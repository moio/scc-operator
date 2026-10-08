// cmd/orgspike/regcodeoveruse/main.go compares the SCC API calls made by the
// two ways of registering systems against a subscription:
//
//	regcode mode (default): per system, POST /connect/subscriptions/systems
//	(announce, authenticated with the regcode) followed by POST
//	/connect/systems/products (activate, authenticated with the system's own
//	credentials, using the regcode as the activation token).
//
//	org-cred bulk mode (--use-org-creds): PUT /connect/organizations/systems
//	in batches, authenticated with org credentials, with each system's
//	product triplet embedded directly in the request. There is no separate
//	activation call — and unlike the regcode path this does NOT consume a
//	subscription slot; the embedded product is only synced onto the system.
//
// The two paths are structurally similar (build a payload, send it, read
// back credentials) but differ in auth, batching granularity, and whether
// activation is an explicit step.
//
// Usage:
//
//	go run ./cmd/orgspike/regcodeoveruse \
//	  --regcode=INTERNAL-USE-ONLY-4041-cd7c --count=5
//
//	go run ./cmd/orgspike/regcodeoveruse --use-org-creds \
//	  --org-username=... --org-password=... --count=20 --batch-size=5
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	flagURL         = flag.String("url", "https://stgscc.suse.com", "SCC base URL (no trailing slash)")
	flagRegcode     = flag.String("regcode", "", "Regcode for regcode mode (or SCC_REGCODE env)")
	flagProductID   = flag.String("product-id", "rancher-prime", "Product identifier")
	flagProductVer  = flag.String("product-ver", "2.14.4", "Product version")
	flagProductArch = flag.String("product-arch", "unknown", "Product arch")
	flagCount       = flag.Int("count", 5, "Number of systems to register")
	flagCleanup     = flag.Bool("cleanup", false, "Deregister created systems after the run")
	flagDelay       = flag.Duration("delay", 100*time.Millisecond, "Pause between regcode-mode requests")
	flagTimeout     = flag.Duration("timeout", 30*time.Second, "HTTP request timeout")

	flagUseOrgCreds = flag.Bool("use-org-creds", false, "Use org-cred bulk mode instead of regcode mode")
	flagOrgUsername = flag.String("org-username", "", "Org credential username (or SCC_ORG_USERNAME env)")
	flagOrgPassword = flag.String("org-password", "", "Org credential password (or SCC_ORG_PASSWORD env)")
	flagBatchSize   = flag.Int("batch-size", 3, "Systems per bulk PUT request in org-cred mode")
)

const (
	apiAccept = "application/json,application/vnd.scc.suse.com.v4+json"
	userAgent = "scc-operator-regcodeoveruse/0.0.1"
)

type product struct {
	ID      string `json:"identifier"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

type system struct {
	Seq      int
	ID       int
	Login    string
	Password string
	Hostname string
}

// ── HTTP ──────────────────────────────────────────────────────────────────

type author func(*http.Request)

func regcodeAuth(code string) author {
	return func(r *http.Request) { r.Header.Set("Authorization", "Token token="+code) }
}

func basicAuth(user, pass string) author {
	return func(r *http.Request) { r.SetBasicAuth(user, pass) }
}

func request(client *http.Client, method, url string, body any, auth author) (status int, respBody []byte, err error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", apiAccept)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	auth(req)

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func pretty(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func newHWInfo(arch string) map[string]any {
	return map[string]any{
		"cpus": 2, "sockets": 1, "arch": arch, "hypervisor": nil, "uuid": uuid.New().String(),
	}
}

func genIdentity(runID string, seq int) (login, password, hostname string) {
	login = fmt.Sprintf("SCC-DEMO-%s-%04d", runID, seq)
	password = uuid.New().String()
	hostname = fmt.Sprintf("scc-demo-%s-%04d.test.invalid", strings.ToLower(runID), seq)
	return
}

// ── regcode mode: announce + activate, one system at a time ────────────────

func announce(client *http.Client, baseURL, regcode string, p product, runID string, seq int) (system, error) {
	login, password, hostname := genIdentity(runID, seq)
	payload := map[string]any{
		"login": login, "password": password, "hostname": hostname,
		"hwinfo": newHWInfo(p.Arch),
	}

	status, body, err := request(client, http.MethodPost, baseURL+"/connect/subscriptions/systems", payload, regcodeAuth(regcode))
	if err != nil {
		return system{}, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return system{}, fmt.Errorf("announce HTTP %d\n%s", status, pretty(body))
	}

	var resp struct {
		ID       int    `json:"id"`
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	_ = json.Unmarshal(body, &resp)
	if resp.Login != "" {
		login = resp.Login
	}
	if resp.Password != "" {
		password = resp.Password
	}
	return system{Seq: seq, ID: resp.ID, Login: login, Password: password, Hostname: hostname}, nil
}

func activate(client *http.Client, baseURL string, sys system, p product, regcode string) error {
	payload := map[string]string{"identifier": p.ID, "version": p.Version, "arch": p.Arch, "token": regcode}
	status, body, err := request(client, http.MethodPost, baseURL+"/connect/systems/products", payload, basicAuth(sys.Login, sys.Password))
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("activate HTTP %d\n%s", status, pretty(body))
	}
	return nil
}

func runRegcodeMode(client *http.Client, baseURL, regcode string, p product, runID string, count int, delay time.Duration) []system {
	systems := make([]system, 0, count)
	for seq := 1; seq <= count; seq++ {
		sys, err := announce(client, baseURL, regcode, p, runID, seq)
		if err != nil {
			fmt.Printf("[%d] announce failed: %v\n", seq, err)
			break
		}
		if err := activate(client, baseURL, sys, p, regcode); err != nil {
			fmt.Printf("[%d] announced id=%d but activate failed: %v\n", seq, sys.ID, err)
			systems = append(systems, sys)
			break
		}
		fmt.Printf("[%d] announced+activated id=%d login=%s\n", seq, sys.ID, sys.Login)
		systems = append(systems, sys)

		if seq < count {
			time.Sleep(delay)
		}
	}
	return systems
}

// ── org-cred mode: bulk PUT, products embedded, no separate activation ─────

type bulkSystem struct {
	Login       string         `json:"login"`
	Password    string         `json:"password"`
	SystemToken string         `json:"system_token"`
	Hostname    string         `json:"hostname"`
	LastSeenAt  string         `json:"last_seen_at"`
	HWInfo      map[string]any `json:"hwinfo"`
	Products    []product      `json:"products,omitempty"`
}

type bulkResponse struct {
	Systems []struct {
		ID       int    `json:"id"`
		Login    string `json:"login"`
		Password string `json:"password"`
	} `json:"systems"`
}

func registerBulk(client *http.Client, baseURL, orgUser, orgPass string, p product, runID string, startSeq, count int) ([]system, error) {
	batch := make([]bulkSystem, count)
	hostnames := make([]string, count)
	for i := range batch {
		login, password, hostname := genIdentity(runID, startSeq+i)
		hostnames[i] = hostname
		batch[i] = bulkSystem{
			Login: login, Password: password, SystemToken: uuid.New().String(),
			Hostname: hostname, LastSeenAt: time.Now().UTC().Format(time.RFC3339),
			HWInfo:   newHWInfo(p.Arch),
			Products: []product{p},
		}
	}

	status, body, err := request(client, http.MethodPut, baseURL+"/connect/organizations/systems",
		struct {
			Systems []bulkSystem `json:"systems"`
		}{batch}, basicAuth(orgUser, orgPass))
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return nil, fmt.Errorf("bulk PUT HTTP %d\n%s", status, pretty(body))
	}

	var resp bulkResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse bulk response: %w", err)
	}

	systems := make([]system, len(resp.Systems))
	for i, rec := range resp.Systems {
		login, password := rec.Login, rec.Password
		if login == "" {
			login = batch[i].Login
		}
		if password == "" {
			password = batch[i].Password
		}
		systems[i] = system{Seq: startSeq + i, ID: rec.ID, Login: login, Password: password, Hostname: hostnames[i]}
	}
	return systems, nil
}

func runOrgCredMode(client *http.Client, baseURL, orgUser, orgPass string, p product, runID string, count, batchSize int) []system {
	systems := make([]system, 0, count)
	for start := 1; start <= count; start += batchSize {
		n := batchSize
		if remaining := count - start + 1; remaining < n {
			n = remaining
		}
		batch, err := registerBulk(client, baseURL, orgUser, orgPass, p, runID, start, n)
		if err != nil {
			fmt.Printf("batch[%d..%d] failed: %v\n", start, start+n-1, err)
			break
		}
		fmt.Printf("batch[%d..%d] created %d system(s)\n", start, start+n-1, len(batch))
		systems = append(systems, batch...)
	}
	return systems
}

// ── cleanup ──────────────────────────────────────────────────────────────

func deregisterSystem(client *http.Client, baseURL string, sys system) (int, []byte, error) {
	return request(client, http.MethodDelete, baseURL+"/connect/systems", nil, basicAuth(sys.Login, sys.Password))
}

func deregisterOrgSystem(client *http.Client, baseURL, orgUser, orgPass string, sys system) (int, []byte, error) {
	url := fmt.Sprintf("%s/connect/organizations/systems/%d", baseURL, sys.ID)
	return request(client, http.MethodDelete, url, nil, basicAuth(orgUser, orgPass))
}

func cleanup(client *http.Client, baseURL, orgUser, orgPass string, useOrgCreds bool, systems []system) {
	for _, sys := range systems {
		var status int
		var err error
		if useOrgCreds {
			status, _, err = deregisterOrgSystem(client, baseURL, orgUser, orgPass, sys)
		} else {
			status, _, err = deregisterSystem(client, baseURL, sys)
		}
		if err != nil {
			fmt.Printf("[%d] cleanup failed: %v\n", sys.Seq, err)
			continue
		}
		fmt.Printf("[%d] deregistered (HTTP %d)\n", sys.Seq, status)
	}
}

// ── main ─────────────────────────────────────────────────────────────────

func valueOrEnv(flagVal, envVar string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envVar)
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	flag.Parse()

	regcode := valueOrEnv(*flagRegcode, "SCC_REGCODE")
	orgUser := valueOrEnv(*flagOrgUsername, "SCC_ORG_USERNAME")
	orgPass := valueOrEnv(*flagOrgPassword, "SCC_ORG_PASSWORD")

	switch {
	case *flagUseOrgCreds && (orgUser == "" || orgPass == ""):
		exitf("--use-org-creds requires org credentials (--org-username/--org-password or SCC_ORG_USERNAME/SCC_ORG_PASSWORD)")
	case !*flagUseOrgCreds && regcode == "":
		exitf("regcode mode requires --regcode or SCC_REGCODE")
	}

	baseURL := strings.TrimRight(*flagURL, "/")
	p := product{ID: *flagProductID, Version: *flagProductVer, Arch: *flagProductArch}
	client := &http.Client{Timeout: *flagTimeout}
	runID := strings.ToUpper(uuid.New().String()[:8])

	fmt.Printf("mode=%s target=%s product=%s/%s/%s count=%d run=%s\n\n",
		map[bool]string{true: "org-cred bulk", false: "regcode serial"}[*flagUseOrgCreds],
		baseURL, p.ID, p.Version, p.Arch, *flagCount, runID)

	var systems []system
	if *flagUseOrgCreds {
		systems = runOrgCredMode(client, baseURL, orgUser, orgPass, p, runID, *flagCount, *flagBatchSize)
	} else {
		systems = runRegcodeMode(client, baseURL, regcode, p, runID, *flagCount, *flagDelay)
	}

	fmt.Printf("\ncreated %d/%d system(s)\n", len(systems), *flagCount)

	if *flagCleanup {
		fmt.Println()
		cleanup(client, baseURL, orgUser, orgPass, *flagUseOrgCreds, systems)
	}
}
