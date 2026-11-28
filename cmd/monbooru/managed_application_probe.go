package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
)

type loaderProbe struct {
	base, csrf string
	client     *http.Client
}

func (p *loaderProbe) request(ctx context.Context, path string, form url.Values, status int) (string, error) {
	method := http.MethodGet
	var body io.Reader
	if form != nil {
		method = http.MethodPost
		form.Set("_csrf", p.csrf)
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return "", err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode != status {
		return "", fmt.Errorf("%s %s: HTTP %d, expected %d", method, path, response.StatusCode, status)
	}
	return string(raw), nil
}

var probeCSRF = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)
var probeGalleryLink = regexp.MustCompile(`href="(/i/[a-f0-9]{64}\?gallery=[A-Za-z_]+)"`)

func withManagedApplication(ctx context.Context, executable, dir string, run func(*loaderProbe) error) error {
	start := time.Now()
	cmd := exec.CommandContext(ctx, executable, "-managed-disposable", dir)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	ready := make(chan string, 1)
	go func() {
		const prefix = "Managed Loader listening at "
		defer close(ready)
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			if value, ok := strings.CutPrefix(lines.Text(), prefix); ok {
				select {
				case ready <- strings.TrimSuffix(value, "/loader"):
				default:
				}
			}
		}
	}()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var base string
	select {
	case base = <-ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	if base == "" {
		return fmt.Errorf("managed Loader stopped before startup")
	}
	p := &loaderProbe{base: base, client: &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	page, err := p.request(ctx, "/loader", nil, http.StatusOK)
	if err != nil {
		return err
	}
	match := probeCSRF.FindStringSubmatch(page)
	if len(match) != 2 || !strings.Contains(page, "Queue import") {
		return fmt.Errorf("managed Loader form is unavailable")
	}
	p.csrf = match[1]
	fmt.Printf("Managed Loader startup through first page %s\n", time.Since(start).Round(time.Millisecond))
	if err := run(p); err != nil {
		return err
	}
	if _, err := p.request(ctx, "/settings/quit", url.Values{}, http.StatusOK); err != nil {
		return err
	}
	return cmd.Wait()
}

func (p *loaderProbe) upload(ctx context.Context, gallery string, data []byte) error {
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("_gallery", gallery); err != nil {
		return err
	}
	file, err := writer.CreateFormFile("files", "existing.png")
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/upload", &form)
	if err != nil {
		return err
	}
	req.Header.Set("X-CSRF-Token", p.csrf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte("1 added")) {
		return fmt.Errorf("could not seed an existing image through Upload: HTTP %d %s", response.StatusCode, raw)
	}
	return nil
}

func probeManagedApplication(ctx context.Context, executable, dir string, setup managedqa.Setup, duplicate []byte) error {
	destination := setup.MetadataFor(1).Gallery
	switchGallery := func(p *loaderProbe, name string) error {
		_, err := p.request(ctx, "/internal/gallery/switch", url.Values{"name": {name}, "return_to": {"/loader"}}, http.StatusSeeOther)
		return err
	}
	submit := func(p *loaderProbe, id int64) error {
		plan := setup.MetadataFor(id)
		_, err := p.request(ctx, "/loader/import", url.Values{
			"url": {plan.URL}, "_gallery": {plan.Gallery}, "max_items": {strconv.Itoa(setup.Count)},
		}, http.StatusSeeOther)
		return err
	}
	if err := withManagedApplication(ctx, executable, dir, func(p *loaderProbe) error {
		if setup.Windows > 1 {
			if err := switchGallery(p, destination); err != nil {
				return err
			}
			if err := p.upload(ctx, destination, duplicate); err != nil {
				return err
			}
		}
		if err := submit(p, 1); err != nil {
			return err
		}
		queue, err := p.request(ctx, "/loader/jobs", nil, http.StatusOK)
		if err != nil || !strings.Contains(queue, "data-live=\"true\"") || !strings.Contains(queue, "<td>queued") {
			return fmt.Errorf("offline submission did not park: %v", err)
		}
		if setup.Destination != "" {
			return switchGallery(p, managedqa.Gallery)
		}
		return nil
	}); err != nil {
		return err
	}
	return withManagedApplication(ctx, executable, dir, func(p *loaderProbe) error {
		queue, err := p.request(ctx, "/loader/jobs", nil, http.StatusOK)
		if err != nil || !strings.Contains(queue, "<td>queued") {
			return fmt.Errorf("queued job did not survive restart: %v", err)
		}
		start := time.Now()
		if _, err := p.request(ctx, "/loader/offline", url.Values{"offline": {"false"}}, http.StatusSeeOther); err != nil {
			return err
		}
		for {
			queue, err := p.request(ctx, "/loader/jobs", nil, http.StatusOK)
			if err != nil {
				return err
			}
			if strings.Contains(queue, "<td>succeeded") {
				break
			}
			if !strings.Contains(queue, "data-live=\"true\"") {
				return fmt.Errorf("managed Loader job stopped before success: %s", queue)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		fmt.Printf("Managed Loader acquisition %d listed in %s\n", setup.Count, time.Since(start).Round(time.Millisecond))
		if _, err := p.request(ctx, "/loader/offline", url.Values{"offline": {"true"}}, http.StatusSeeOther); err != nil {
			return err
		}
		results, err := p.request(ctx, "/loader/jobs/1/results", nil, http.StatusOK)
		if err != nil {
			return err
		}
		links := probeGalleryLink.FindAllStringSubmatch(results, -1)
		wantLinks := min(50, setup.Count)
		if setup.Count > 1 {
			wantLinks--
		}
		if len(links) != wantLinks || strings.Contains(results, ">Blacklisted<") != (setup.Count > 1) {
			return fmt.Errorf("unexpected managed Loader results: %d gallery links", len(links))
		}
		for _, link := range links {
			if !strings.HasSuffix(link[1], "?gallery="+destination) {
				return fmt.Errorf("result linked outside the submitted gallery")
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+links[0][1], nil)
		if err != nil {
			return err
		}
		response, err := p.client.Do(req)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusSeeOther && response.StatusCode != http.StatusFound {
			return fmt.Errorf("gallery link unavailable offline: HTTP %d", response.StatusCode)
		}
		location, err := response.Location()
		if err != nil {
			return err
		}
		detail, err := p.request(ctx, location.RequestURI(), nil, http.StatusOK)
		if err != nil || !strings.Contains(detail, "fixture") || !strings.Contains(detail, "general") {
			return fmt.Errorf("imported rating or tags unavailable in offline gallery: %v", err)
		}
		if setup.Windows > 1 {
			if !strings.Contains(results, ">duplicate<") || !strings.Contains(results, ">created<") {
				return fmt.Errorf("expected an existing image and a new image in Loader results")
			}
			if err := switchGallery(p, destination); err != nil {
				return err
			}
			if err := submit(p, 2); err != nil {
				return err
			}
			if _, err := p.request(ctx, "/loader/jobs/2/cancel", url.Values{}, http.StatusSeeOther); err != nil {
				return err
			}
			queue, err := p.request(ctx, "/loader/jobs", nil, http.StatusOK)
			if err != nil || !strings.Contains(queue, "<td>canceled") {
				return fmt.Errorf("parked job cancellation not visible: %v", err)
			}
		}
		fmt.Printf("PASS: application Loader imported %d posts, with durable queue restart, offline browsing and shutdown\n", len(setup.ExpectedIDs(1)))
		return nil
	})
}

func probeActiveManagedCancel(ctx context.Context, executable, dir string, setup managedqa.Setup) error {
	return withManagedApplication(ctx, executable, dir, func(p *loaderProbe) error {
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		submit := func(id int64) error {
			plan := setup.MetadataFor(id)
			_, err := p.request(ctx, "/loader/import", url.Values{
				"url": {plan.URL}, "_gallery": {plan.Gallery}, "max_items": {strconv.Itoa(setup.Count)},
			}, http.StatusSeeOther)
			return err
		}
		wait := func(check func() (bool, error)) error {
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				done, err := check()
				if err != nil || done {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tick.C:
				}
			}
		}
		if _, err := p.request(ctx, "/loader/offline", url.Values{"offline": {"false"}}, http.StatusSeeOther); err != nil {
			return err
		}
		if err := submit(1); err != nil {
			return err
		}
		if err := wait(func() (bool, error) {
			_, err := os.Stat(filepath.Join(dir, "cancel-fixture-ready"))
			if os.IsNotExist(err) {
				return false, nil
			}
			return err == nil, err
		}); err != nil {
			return fmt.Errorf("wait for active metadata: %w", err)
		}
		if _, err := p.request(ctx, "/loader/jobs/1/cancel", url.Values{}, http.StatusSeeOther); err != nil {
			return err
		}
		if err := wait(func() (bool, error) {
			queue, err := p.request(ctx, "/loader/jobs", nil, http.StatusOK)
			return strings.Contains(queue, "<td>canceled"), err
		}); err != nil {
			return fmt.Errorf("wait for canceled job: %w", err)
		}
		if err := submit(2); err != nil {
			return err
		}
		if err := wait(func() (bool, error) {
			queue, err := p.request(ctx, "/loader/jobs", nil, http.StatusOK)
			return strings.Contains(queue, "<td>succeeded"), err
		}); err != nil {
			return fmt.Errorf("wait for next job: %w", err)
		}
		fmt.Println("PASS: canceled active metadata and imported the next job in the same application session")
		return nil
	})
}
