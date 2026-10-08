// cmd/orgspike/main.go — Option 0 spike (throwaway).
//
// Probes the four PLAN.md questions plus a now-confirmed follow-up (Q2c):
//
//  Q1.  GET  /connect/organizations/subscriptions
//           → do Rancher Prime subscriptions carry a regcode?
//
//  Q2.  POST /connect/organizations/systems  (single system, with products[])
//           → system created, but products[] in this payload is a SYNC field
//             (RMT-style telemetry of already-activated products), NOT an
//             activation call.  Subscription systems_count does NOT increment.
//
//  Verify.  GET  /connect/organizations/systems/:id
//           GET  /connect/systems/activations  (system creds)
//           GET  /connect/organizations/subscriptions  (recheck systems_count)
//           → confirms Q2 created the system but left activations empty.
//
//  Q2c. POST /connect/systems/products  {identifier, version, arch, token=<regcode>}
//           → THIS is the actual activation call.  Consumes a regcode slot,
//             creates a countable activation.  Uses system creds from Q2.
//
//  Q2b. PUT  /connect/organizations/systems  {"systems":[...]}
//           → validates the bulk-of-one path Option 3 would use.
//
//  Q3.  GET  /connect/systems/activations   (system creds)
//       PUT  /connect/systems               (keepalive)
//           → confirms system creds from Q2 authenticate against system-level
//             endpoints before AND after activation.
//
//  Q4.  DELETE /connect/organizations/systems/:id
//           → cleanup.
//
// Usage:
//
//	SCC_ORG_USERNAME=... SCC_ORG_PASSWORD=... go run ./cmd/orgspike/ \
//	  --url=https://scc.suse.com \
//	  --product-id=rancher-prime --product-ver=2.9 --product-arch=x86_64
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ── flags ─────────────────────────────────────────────────────────────────────

var (
	flagURL         = flag.String("url", "https://stgscc.suse.com", "SCC base URL (no trailing slash)")
	flagUsername    = flag.String("username", "", "Org credential username (or SCC_ORG_USERNAME env)")
	flagPassword    = flag.String("password", "", "Org credential password (or SCC_ORG_PASSWORD env)")
	flagProductID   = flag.String("product-id", "rancher-prime", "Rancher Prime SCC product identifier")
	flagProductVer  = flag.String("product-ver", "2.14.4", "Product version for the triplet (must be a concrete release in the catalog)")
	flagProductArch = flag.String("product-arch", "unknown", "Product arch. Rancher telemetry reports the literal string 'unknown'; SCC keys the rancher-prime product on it.")
	flagSkipQ2      = flag.Bool("skip-q2", false, "Skip Q2+ (safe read-only mode)")
	flagNoCleanup   = flag.Bool("no-cleanup", false, "Leave created systems in SCC for manual inspection")
	// A stable UUID that identifies this spike as the registration proxy.
	spikeProxyUUID = uuid.New().String()
)

// ── wire types ────────────────────────────────────────────────────────────────

type orgSubscription struct {
	ID             int      `json:"id"`
	RegCode        string   `json:"regcode"`
	Name           string   `json:"name"`
	Kind           string   `json:"type"`
	Status         string   `json:"status"`
	StartsAt       string   `json:"starts_at"`
	ExpiresAt      string   `json:"expires_at"`
	SystemLimit    int      `json:"system_limit"`
	SystemsCount   int      `json:"systems_count"`
	VirtualCount   int      `json:"virtual_count"`
	ProductClasses []string `json:"product_classes"`
	Families       []string `json:"families"`
}

type orgProduct struct {
	ID          int    `json:"id,omitempty"`
	Identifier  string `json:"identifier"`
	Version     string `json:"version"`
	Arch        string `json:"arch"`
	RegCode     string `json:"regcode,omitempty"`
	ActivatedAt string `json:"activated_at,omitempty"`
}

type orgSystemPayload struct {
	Login       string         `json:"login"`
	Password    string         `json:"password"`
	SystemToken string         `json:"system_token"`
	Hostname    string         `json:"hostname"`
	LastSeenAt  string         `json:"last_seen_at"`
	HWInfo      map[string]any `json:"hwinfo"`
	Products    []orgProduct   `json:"products,omitempty"`
}

type orgSystemsWrap struct {
	Systems []orgSystemPayload `json:"systems"`
}

type orgSystemRecord struct {
	ID          int    `json:"id"`
	Login       string `json:"login"`
	Password    string `json:"password"`
	SystemToken string `json:"system_token"`
	LastSeenAt  string `json:"last_seen_at"`
}

type orgSystemsResponse struct {
	Systems []orgSystemRecord `json:"systems"`
}

// sysActivation mirrors the system_activation schema.
type sysActivation struct {
	ID       int    `json:"id"`
	RegCode  string `json:"regcode"`
	Name     string `json:"name"`
	Kind     string `json:"type"`
	Status   string `json:"status"`
	StartsAt string `json:"starts_at"`
	ExpiresAt string `json:"expires_at"`
	SystemID int    `json:"system_id"`
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

const apiAccept = "application/json,application/vnd.scc.suse.com.v4+json"

func newClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

type reqOpts struct {
	method   string
	url      string
	orgUser  string
	orgPass  string
	sysUser  string // non-empty → use system Basic auth
	sysPass  string
	regcode  string // non-empty → use Token token=<regcode> auth
	body     any
	rmt      string // RMT proxy UUID header
}

func doReq(client *http.Client, o reqOpts) (status int, body []byte, headers http.Header, err error) {
	var r io.Reader
	if o.body != nil {
		b, e := json.Marshal(o.body)
		if e != nil {
			return 0, nil, nil, fmt.Errorf("marshal: %w", e)
		}
		r = bytes.NewReader(b)
	}
	req, e := http.NewRequest(o.method, o.url, r)
	if e != nil {
		return 0, nil, nil, e
	}
	req.Header.Set("Accept", apiAccept)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "scc-operator-orgspike/0.0.1")
	switch {
	case o.sysUser != "":
		req.SetBasicAuth(o.sysUser, o.sysPass)
	case o.regcode != "":
		req.Header.Set("Authorization", "Token token="+o.regcode)
	case o.orgUser != "":
		req.SetBasicAuth(o.orgUser, o.orgPass)
	}
	if o.rmt != "" {
		req.Header.Set("RMT", o.rmt)
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, nil, nil, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp.Header, e
}

func parseLinkNext(h string) string {
	if m := regexp.MustCompile(`<([^>]+)>;\s*rel="next"`).FindStringSubmatch(h); len(m) == 2 {
		return m[1]
	}
	return ""
}

func pretty(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "    " + string(raw)
	}
	b, _ := json.MarshalIndent(v, "    ", "  ")
	return "    " + string(b)
}

// ── Q0 — product discovery ──────────────────────────────────────────────────

// qDiscoverProduct calls GET /connect/subscriptions/products for each Rancher
// subscription found in Q1, printing the full product triplets covered by each
// regcode.  It returns the best matching base-product triplet and the regcode
// that covers it, so subsequent probes use the correct identifier + regcode.
// Matching logic: prefer exact identifier match on --product-id, then prefer
// versions that start with --product-ver (e.g. "2.9" → "2.9.5").
func qDiscoverProduct(client *http.Client, baseURL, wantID, wantVer, fallbackArch string, subs []orgSubscription) (identifier, version, arch, regcode string) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Q0: GET /connect/subscriptions/products  (per Rancher regcode)")
	fmt.Println("    → discover the correct product triplet for this org")
	fmt.Println("════════════════════════════════════════════════════════════")

	type productRecord struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
		Version    string `json:"version"`
		Arch       string `json:"arch"`
		ProductType string `json:"product_type"`
		FriendlyName string `json:"friendly_name"`
		Free       bool   `json:"free"`
		ProductClass string `json:"product_class"`
	}

	for _, sub := range subs {
		fmt.Printf("\n  Subscription id=%-8d  %q\n", sub.ID, sub.Name)
		fmt.Printf("  regcode=%q\n", sub.RegCode)

		nextURL := baseURL + "/connect/subscriptions/products"
		var products []productRecord
		for page := 1; nextURL != ""; page++ {
			status, body, hdrs, err := doReq(client, reqOpts{
				method:  "GET",
				url:     nextURL,
				regcode: sub.RegCode,
			})
			if err != nil {
				fmt.Printf("  ✗ page %d: %v\n", page, err)
				break
			}
			if status != 200 {
				fmt.Printf("  HTTP %d\n%s\n", status, pretty(body))
				break
			}
			var page []productRecord
			if err := json.Unmarshal(body, &page); err != nil {
				fmt.Printf("  ✗ parse: %v\n", err)
				break
			}
			products = append(products, page...)
			nextURL = parseLinkNext(hdrs.Get("Link"))
		}

		if len(products) == 0 {
			fmt.Println("  (no products returned)")
			continue
		}
		fmt.Printf("  %d product(s):\n", len(products))
		for _, p := range products {
			marker := " "
			if p.ProductType == "base" {
				marker = "*" // base products are the ones we activate
			}
			fmt.Printf("  %s id=%-6d  %-10s  %-42s  %s/%s/%s\n",
				marker, p.ID, p.ProductType, p.FriendlyName,
				p.Identifier, p.Version, p.Arch)
			// Selection: exact identifier match + version-prefix match wins;
			// then exact identifier match (any version);
			// then first non-free base product as fallback.
			if p.ProductType != "base" || p.Free {
				continue
			}
			exactID := p.Identifier == wantID
			verPrefix := strings.HasPrefix(p.Version, wantVer)
			switch {
			case exactID && verPrefix:
				// Best possible match — use it and stop scanning this subscription.
				identifier = p.Identifier
				version = p.Version
				arch = p.Arch
				regcode = sub.RegCode
			case exactID && identifier == "":
				// Exact identifier but wrong version — hold as candidate.
				identifier = p.Identifier
				version = p.Version
				arch = p.Arch
				regcode = sub.RegCode
			case identifier == "":
				// No match yet at all — take anything as last resort.
				identifier = p.Identifier
				version = p.Version
				arch = p.Arch
				regcode = sub.RegCode
			}
		}
	}

	fmt.Println()
	if identifier != "" {
		// The subscription-products catalog reports an EMPTY arch for every
		// Rancher product. But POST /connect/systems/products requires a
		// concrete, non-empty arch that SCC keys the product on — for Rancher
		// that is the literal string "unknown" (what Rancher telemetry sends).
		// So never trust the catalog arch; force the caller-provided arch.
		if arch == "" {
			arch = fallbackArch
			fmt.Printf("  (catalog arch empty — using fallback arch %q for activation)\n", arch)
		}
		fmt.Printf("  ✔ Q0: will use triplet  %s / %s / %s\n", identifier, version, arch)
	} else {
		fmt.Println("  ✗ Q0: no base product found — will use --product-id flag value as-is.")
	}
	return
}

// ── Q1 ────────────────────────────────────────────────────────────────────────

func q1Subscriptions(client *http.Client, baseURL, user, pass, productID string) (matched []orgSubscription) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Q1: GET /connect/organizations/subscriptions")
	fmt.Println("════════════════════════════════════════════════════════════")

	nextURL := baseURL + "/connect/organizations/subscriptions"
	var all []orgSubscription
	for page := 1; nextURL != ""; page++ {
		fmt.Printf("  page %d: %s\n", page, nextURL)
		status, body, hdrs, err := doReq(client, reqOpts{method: "GET", url: nextURL, orgUser: user, orgPass: pass})
		if err != nil {
			fmt.Printf("  ✗ %v\n", err)
			return
		}
		fmt.Printf("  HTTP %d\n", status)
		if status != 200 {
			fmt.Printf("%s\n", pretty(body))
			return
		}
		var page []orgSubscription
		if err := json.Unmarshal(body, &page); err != nil {
			fmt.Printf("  ✗ parse: %v\n  raw: %s\n", err, string(body))
			return
		}
		all = append(all, page...)
		nextURL = parseLinkNext(hdrs.Get("Link"))
	}

	fmt.Printf("\n  Total subscriptions: %d\n", len(all))
	for i, s := range all {
		fmt.Printf("\n  [%d] id=%-8d  status=%-8s  name=%q\n", i+1, s.ID, s.Status, s.Name)
		fmt.Printf("      type=%-12s  expires=%s\n", s.Kind, s.ExpiresAt)
		fmt.Printf("      system_limit=%-4d  systems_count=%d\n", s.SystemLimit, s.SystemsCount)
		fmt.Printf("      regcode=%q\n", s.RegCode)
		fmt.Printf("      product_classes=%v\n", s.ProductClasses)

		pidUp := strings.ToUpper(productID)
		nameUp := strings.ToUpper(s.Name)
		isRancher := strings.Contains(nameUp, "RANCHER")
		for _, pc := range s.ProductClasses {
			if strings.Contains(strings.ToUpper(pc), "RANCHER") || strings.Contains(strings.ToUpper(pc), pidUp) {
				isRancher = true
			}
		}
		if isRancher {
			matched = append(matched, s)
		}
	}
	fmt.Println()
	if len(matched) > 0 {
		fmt.Printf("  ✔ Q1: %d Rancher subscription(s) found with regcodes.\n", len(matched))
	} else {
		fmt.Println("  ✗ Q1: no Rancher subscriptions found (check credentials / product-id flag).")
	}
	return
}

// ── Q2 POST ───────────────────────────────────────────────────────────────────

func q2CreateSystem(client *http.Client, baseURL, orgUser, orgPass, productID, productVer, productArch string) (systemID int, sysLogin, sysPass string, ok bool) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Q2: POST /connect/organizations/systems")
	fmt.Println("    (note: products[] here is a SYNC field, not activation)")
	fmt.Println("════════════════════════════════════════════════════════════")

	spikeID := uuid.New().String()[:8]
	sysLogin = fmt.Sprintf("SCC-SPIKE-%s", strings.ToUpper(spikeID))
	sysPass = uuid.New().String()

	payload := orgSystemPayload{
		Login:       sysLogin,
		Password:    sysPass,
		SystemToken: uuid.New().String(),
		Hostname:    fmt.Sprintf("scc-operator-spike-%s.test.invalid", spikeID),
		LastSeenAt:  time.Now().UTC().Format(time.RFC3339),
		HWInfo: map[string]any{
			"cpus": 2, "sockets": 1, "arch": productArch,
			"hypervisor": nil, "uuid": uuid.New().String(),
		},
		Products: []orgProduct{
			{Identifier: productID, Version: productVer, Arch: productArch},
		},
	}

	b, _ := json.MarshalIndent(payload, "  ", "  ")
	fmt.Printf("  Request body:\n  %s\n\n", string(b))

	status, body, _, err := doReq(client, reqOpts{
		method: "POST", url: baseURL + "/connect/organizations/systems",
		orgUser: orgUser, orgPass: orgPass, body: payload, rmt: spikeProxyUUID,
	})
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
		return
	}
	fmt.Printf("  HTTP %d\n%s\n", status, pretty(body))

	if status != 201 && status != 200 {
		fmt.Printf("  ✗ Q2: FAILED — HTTP %d\n", status)
		return
	}
	var rec orgSystemRecord
	if err := json.Unmarshal(body, &rec); err == nil && rec.ID != 0 {
		systemID = rec.ID
		if rec.Login != "" {
			sysLogin = rec.Login
		}
		if rec.Password != "" {
			sysPass = rec.Password
		}
	}
	fmt.Printf("  ✔ Q2: system created. id=%d  login=%s\n", systemID, sysLogin)
	ok = true
	return
}

// ── Verify ────────────────────────────────────────────────────────────────────

// qVerify checks what SCC actually recorded for the Q2 system, and whether
// the subscription systems_count moved.  Expected: products absent, count unchanged.
func qVerify(client *http.Client, baseURL, orgUser, orgPass, sysUser, sysPass string, systemID int, subs []orgSubscription) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Verify: state immediately after Q2 (before any activation)")
	fmt.Println("════════════════════════════════════════════════════════════")

	// 1. GET /connect/organizations/systems/:id
	url := fmt.Sprintf("%s/connect/organizations/systems/%d", baseURL, systemID)
	fmt.Printf("\n  1. GET %s\n", url)
	status, body, _, err := doReq(client, reqOpts{method: "GET", url: url, orgUser: orgUser, orgPass: orgPass})
	if err != nil {
		fmt.Printf("     ✗ %v\n", err)
	} else {
		fmt.Printf("     HTTP %d\n%s\n", status, pretty(body))
		if status == 200 {
			fmt.Println("     → SCC system record retrieved (check for 'products' key absence above).")
		}
	}

	// 2. GET /connect/systems/activations (system creds)
	url = baseURL + "/connect/systems/activations"
	fmt.Printf("\n  2. GET %s  (system creds)\n", url)
	status, body, _, err = doReq(client, reqOpts{method: "GET", url: url, sysUser: sysUser, sysPass: sysPass})
	if err != nil {
		fmt.Printf("     ✗ %v\n", err)
	} else {
		fmt.Printf("     HTTP %d\n%s\n", status, pretty(body))
		var acts []sysActivation
		if status == 200 && json.Unmarshal(body, &acts) == nil {
			if len(acts) == 0 {
				fmt.Println("     → [] no activation record.")
			} else {
				linked := false
				for _, a := range acts {
					if a.RegCode != "" || a.Status != "" {
						linked = true
					}
				}
				if linked {
					fmt.Printf("     → %d activation(s), subscription-LINKED (regcode/status set).\n", len(acts))
				} else {
					fmt.Printf("     → %d activation(s), but UNLINKED (regcode=null, status=null).\n", len(acts))
					fmt.Println("       i.e. org POST products[] recorded the product WITHOUT consuming a subscription slot.")
				}
			}
		}
	}

	// 3. Re-fetch subscriptions to check systems_count
	fmt.Println("\n  3. Re-checking subscription systems_count …")
	for _, sub := range subs {
		url := fmt.Sprintf("%s/connect/organizations/subscriptions", baseURL)
		// The paginated list includes all — just re-fetch page 1 and scan by ID.
		_, body, _, err := doReq(client, reqOpts{method: "GET", url: url, orgUser: orgUser, orgPass: orgPass})
		if err != nil {
			break
		}
		var fresh []orgSubscription
		if json.Unmarshal(body, &fresh) != nil {
			break
		}
		for _, fs := range fresh {
			if fs.ID == sub.ID {
				delta := fs.SystemsCount - sub.SystemsCount
				if delta == 0 {
					fmt.Printf("     sub id=%-8d  systems_count=%d  (unchanged — confirmed: org POST does not consume a slot)\n", fs.ID, fs.SystemsCount)
				} else {
					fmt.Printf("     sub id=%-8d  systems_count=%d  (delta=%+d — unexpected)\n", fs.ID, fs.SystemsCount, delta)
				}
			}
		}
		break // only need one page for this check
	}

	fmt.Println("\n  ── Diagnosis ───────────────────────────────────────────────")
	fmt.Println("  POST/PUT /connect/organizations/systems with a valid products[] triplet")
	fmt.Println("  DOES create an activation RECORD, but it is UNLINKED: regcode=null,")
	fmt.Println("  status=null, and it does NOT increment the subscription systems_count.")
	fmt.Println("  (With an INVALID triplet the products[] entry is silently ignored.)")
	fmt.Println("  A subscription-counted activation REQUIRES the regcode flow:")
	fmt.Println("    POST /connect/systems/products  {identifier,version,arch,token=<regcode>}")
	fmt.Println("  Critical: arch must be the literal 'unknown' (what Rancher telemetry sends);")
	fmt.Println("  x86_64/empty/null all 422 with 'No product found'.")
}

// ── Q2c — actual activation ───────────────────────────────────────────────────

// q2cActivate calls POST /connect/systems/products with the system credentials
// from Q2 and a regcode obtained in Q1.  This is the real activation call.
func q2cActivate(client *http.Client, baseURL, sysUser, sysPass, productID, productVer, productArch, regcode string) (ok bool) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Q2c: POST /connect/systems/products  {identifier,version,arch,token=<regcode>}")
	fmt.Println("     (the REAL activation call — consumes a subscription slot)")
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("  Using regcode: %q\n", regcode)

	payload := map[string]string{
		"identifier": productID,
		"version":    productVer,
		"arch":       productArch,
		"token":      regcode,
	}
	b, _ := json.MarshalIndent(payload, "  ", "  ")
	fmt.Printf("  Request body:\n  %s\n\n", string(b))

	status, body, _, err := doReq(client, reqOpts{
		method:  "POST",
		url:     baseURL + "/connect/systems/products",
		sysUser: sysUser,
		sysPass: sysPass,
		body:    payload,
	})
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
		return
	}
	fmt.Printf("  HTTP %d\n%s\n", status, pretty(body))

	switch status {
	case 201:
		fmt.Println("  ✔ Q2c: activation created (201). Subscription slot consumed.")
		ok = true
	case 422:
		fmt.Println("  ✗ Q2c: 422 — 'No valid subscription found' or bad product triplet.")
		fmt.Println("    Possible causes:")
		fmt.Println("    • product identifier / version / arch doesn't match SCC's record")
		fmt.Println("    • this org's subscriptions don't cover this product triplet")
		fmt.Println("    • regcode expired or at system_limit")
	default:
		fmt.Printf("  ✗ Q2c: HTTP %d\n", status)
	}
	return
}

// ── Q2d — announce via subscriptions/systems + custom creds ─────────────────

// q2dAnnounce tests POST /connect/subscriptions/systems using a regcode for
// authentication.  This is the normal announce path that DOES associate the
// system with a subscription, enabling subsequent activation.
// It also probes whether SCC accepts caller-chosen login/password (field is
// optional per spec — key question for Option 3 credential minting).
func q2dAnnounce(client *http.Client, baseURL, regcode, productArch string) (systemID int, sysLogin, sysPass string, ok bool) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Q2d: POST /connect/subscriptions/systems  (normal announce, regcode auth)")
	fmt.Println("     with caller-chosen login+password  → does SCC accept them?")
	fmt.Println("════════════════════════════════════════════════════════════")

	spikeID := uuid.New().String()[:8]
	sysLogin = fmt.Sprintf("SCC-SPIKE-D-%s", strings.ToUpper(spikeID))
	sysPass = uuid.New().String()

	payload := map[string]any{
		"login":    sysLogin,
		"password": sysPass,
		"hostname": fmt.Sprintf("scc-operator-spikd-%s.test.invalid", spikeID),
		"hwinfo": map[string]any{
			"cpus":       2,
			"sockets":    1,
			"arch":       productArch,
			"hypervisor": nil,
			"uuid":       uuid.New().String(),
		},
	}
	b, _ := json.MarshalIndent(payload, "  ", "  ")
	fmt.Printf("  Using regcode: %q\n", regcode)
	fmt.Printf("  Proposed login: %s\n", sysLogin)
	fmt.Printf("  Request body:\n  %s\n\n", string(b))

	status, body, _, err := doReq(client, reqOpts{
		method:  "POST",
		url:     baseURL + "/connect/subscriptions/systems",
		regcode: regcode,
		body:    payload,
	})
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
		return
	}
	fmt.Printf("  HTTP %d\n%s\n", status, pretty(body))

	type announceResp struct {
		ID       int    `json:"id"`
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	var resp announceResp
	_ = json.Unmarshal(body, &resp)

	if status != 201 && status != 200 {
		fmt.Printf("  ✗ Q2d: FAILED — HTTP %d\n", status)
		return
	}

	ok = true
	systemID = resp.ID
	if resp.Login != "" {
		sysLogin = resp.Login
	}
	if resp.Password != "" {
		sysPass = resp.Password
	}

	switch {
	case resp.Login == payload["login"].(string):
		fmt.Printf("  ✔ Q2d: HTTP %d — system announced. id=%d\n", status, systemID)
		fmt.Printf("  ✔ SCC accepted our proposed login=%s (custom credentials WORK → Option 3 viable via announce path)\n", sysLogin)
	case resp.Login != "":
		fmt.Printf("  ✔ Q2d: HTTP %d — system announced. id=%d\n", status, systemID)
		fmt.Printf("  ⚠ SCC replaced our login with %s (custom credentials IGNORED → Option 3 must use SCC-generated creds)\n", resp.Login)
	default:
		fmt.Printf("  ✔ Q2d: HTTP %d — system announced. id=%d (login not in response)\n", status, systemID)
	}
	return
}

// q2dActivate calls POST /connect/systems/products for a system created via Q2d.
// arch order puts "unknown" first because that is what Rancher telemetry reports
// and what SCC keys the rancher-prime product on. The others are kept only to
// demonstrate they fail (diagnostic value).
func q2dActivate(client *http.Client, baseURL, sysLogin, sysPass, productID, productVer, productArch, regcode string) (activated bool) {
	fmt.Println("\n────────────────────────────────────────────────────────────")
	fmt.Println("Q2d-act: POST /connect/systems/products  (arch-ordered: unknown first)")
	fmt.Println("────────────────────────────────────────────────────────────")

	// Put the caller's arch (default "unknown") first; keep a couple of wrong
	// values afterwards purely to show they 422, proving arch is the key.
	seen := map[string]bool{}
	var archCandidates []string
	for _, a := range []string{productArch, "unknown", "x86_64"} {
		if !seen[a] {
			seen[a] = true
			archCandidates = append(archCandidates, a)
		}
	}

	for _, arch := range archCandidates {
		fmt.Printf("\n  trying %s/%s/%s + regcode\n", productID, productVer, arch)
		payload := map[string]string{
			"identifier": productID,
			"version":    productVer,
			"arch":       arch,
			"token":      regcode,
		}
		status, body, _, err := doReq(client, reqOpts{
			method:  "POST",
			url:     baseURL + "/connect/systems/products",
			sysUser: sysLogin,
			sysPass: sysPass,
			body:    payload,
		})
		if err != nil {
			fmt.Printf("  ✗ error: %v\n", err)
			continue
		}
		switch status {
		case 201:
			fmt.Printf("  ✔ HTTP 201 — ACTIVATION SUCCESS with arch=%q\n", arch)
			var svc struct {
				Name    string `json:"name"`
				Product struct {
					Identifier string `json:"identifier"`
					Version    string `json:"version"`
					Arch       any    `json:"arch"`
				} `json:"product"`
			}
			_ = json.Unmarshal(body, &svc)
			fmt.Printf("     service=%s product=%s/%s (stored arch=%v)\n",
				svc.Name, svc.Product.Identifier, svc.Product.Version, svc.Product.Arch)
			fmt.Println("  └─ FULL FLOW CONFIRMED: org creds → regcode → announce → activate.")
			return true // stop on first success
		case 422:
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &e)
			fmt.Printf("  ✗ HTTP 422: %s\n", e.Error)
		default:
			fmt.Printf("  ✗ HTTP %d\n%s\n", status, pretty(body))
		}
	}

	fmt.Println("\n  ✗ No arch variant activated. If even 'unknown' failed, the version")
	fmt.Println("    is likely not a concrete catalog release (try --product-ver=2.14.4)")
	fmt.Println("    or the regcode's subscription does not cover this product.")
	return false
}

func q2bBulkPUT(client *http.Client, baseURL, orgUser, orgPass, productID, productVer, productArch string) (systemID int) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Q2b: PUT /connect/organizations/systems {\"systems\":[...]}  (bulk-of-one)")
	fmt.Println("════════════════════════════════════════════════════════════")

	spikeID := uuid.New().String()[:8]
	wrap := orgSystemsWrap{Systems: []orgSystemPayload{{
		Login:       fmt.Sprintf("SCC-SPIKEB-%s", strings.ToUpper(spikeID)),
		Password:    uuid.New().String(),
		SystemToken: uuid.New().String(),
		Hostname:    fmt.Sprintf("scc-operator-spikeb-%s.test.invalid", spikeID),
		LastSeenAt:  time.Now().UTC().Format(time.RFC3339),
		HWInfo:      map[string]any{"cpus": 2, "sockets": 1, "arch": productArch, "hypervisor": nil, "uuid": uuid.New().String()},
		Products:    []orgProduct{{Identifier: productID, Version: productVer, Arch: productArch}},
	}}}

	status, body, _, err := doReq(client, reqOpts{
		method: "PUT", url: baseURL + "/connect/organizations/systems",
		orgUser: orgUser, orgPass: orgPass, body: wrap, rmt: spikeProxyUUID,
	})
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
		return
	}
	fmt.Printf("  HTTP %d\n%s\n", status, pretty(body))

	if status == 201 || status == 200 {
		var resp orgSystemsResponse
		if err := json.Unmarshal(body, &resp); err == nil && len(resp.Systems) > 0 {
			systemID = resp.Systems[0].ID
			fmt.Printf("  ✔ Q2b: bulk-of-one succeeded. id=%d  login=%s\n", systemID, resp.Systems[0].Login)
		}
	} else {
		fmt.Printf("  ✗ Q2b: HTTP %d\n", status)
	}
	return
}

// ── Q3 ────────────────────────────────────────────────────────────────────────

func q3SystemAuth(client *http.Client, baseURL, login, password string) {
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("Q3: system-credential auth (after Q2c activation)")
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("  login=%s\n", login)

	// 3a activations — should now be non-empty
	url := baseURL + "/connect/systems/activations"
	fmt.Printf("\n  3a. GET %s\n", url)
	status, body, _, err := doReq(client, reqOpts{method: "GET", url: url, sysUser: login, sysPass: password})
	if err != nil {
		fmt.Printf("      ✗ %v\n", err)
	} else {
		fmt.Printf("      HTTP %d\n%s\n", status, pretty(body))
		var acts []sysActivation
		if status == 200 && json.Unmarshal(body, &acts) == nil {
			switch len(acts) {
			case 0:
				fmt.Println("      ✗ still empty — Q2c did not produce an activation.")
			default:
				fmt.Printf("      ✔ %d activation(s) — activation confirmed.\n", len(acts))
			}
		}
	}

	// 3b keepalive
	url = baseURL + "/connect/systems"
	fmt.Printf("\n  3b. PUT %s  (keepalive)\n", url)
	status, body, _, err = doReq(client, reqOpts{
		method: "PUT", url: url, sysUser: login, sysPass: password,
		body: map[string]any{
			"hostname": fmt.Sprintf("%s-keepalive.test.invalid", login),
			"hwinfo":   map[string]any{"cpus": 2, "sockets": 1, "arch": "x86_64", "hypervisor": nil},
		},
	})
	if err != nil {
		fmt.Printf("      ✗ %v\n", err)
	} else {
		fmt.Printf("      HTTP %d", status)
		if len(body) > 0 {
			fmt.Printf("\n%s", pretty(body))
		}
		fmt.Println()
		if status == 204 || status == 200 {
			fmt.Println("      ✔ keepalive OK.")
		} else {
			fmt.Printf("      ✗ HTTP %d\n", status)
		}
	}
}

// ── Q4 ────────────────────────────────────────────────────────────────────────

func q4Delete(client *http.Client, baseURL, orgUser, orgPass string, systemID int, label string) {
	fmt.Printf("\n════════════════════════════════════════════════════════════\n")
	fmt.Printf("Q4: DELETE /connect/organizations/systems/%d  (%s)\n", systemID, label)
	fmt.Println("════════════════════════════════════════════════════════════")
	if systemID == 0 {
		fmt.Println("  ⚠ no system ID — skipped.")
		return
	}
	url := fmt.Sprintf("%s/connect/organizations/systems/%d", baseURL, systemID)
	status, body, _, err := doReq(client, reqOpts{method: "DELETE", url: url, orgUser: orgUser, orgPass: orgPass, rmt: spikeProxyUUID})
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
		return
	}
	fmt.Printf("  HTTP %d", status)
	if len(body) > 0 {
		fmt.Printf("\n%s", pretty(body))
	}
	fmt.Println()
	switch {
	case status == 204:
		fmt.Println("  ✔ 204 No Content — deleted cleanly.")
	case status == 200:
		fmt.Println("  ✔ 200 OK — deleted.")
	case status == 404:
		fmt.Println("  ⚠ 404 — already gone.")
	default:
		fmt.Printf("  ✗ HTTP %d\n", status)
	}
}

// q4DeregisterSystem removes an announce-created system via its own credentials.
// The org DELETE endpoint 404s for these; only DELETE /connect/systems works.
func q4DeregisterSystem(client *http.Client, baseURL, sysLogin, sysPass string, systemID int) {
	fmt.Printf("\n════════════════════════════════════════════════════════════\n")
	fmt.Printf("Q4: DELETE /connect/systems  (Q2d, system-owned, id=%d)\n", systemID)
	fmt.Println("════════════════════════════════════════════════════════════")
	status, _, _, err := doReq(client, reqOpts{
		method:  "DELETE",
		url:     baseURL + "/connect/systems",
		sysUser: sysLogin,
		sysPass: sysPass,
	})
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
		return
	}
	switch status {
	case 204, 200:
		fmt.Printf("  ✔ HTTP %d — deregistered, subscription slot freed.\n", status)
	default:
		fmt.Printf("  ✗ HTTP %d\n", status)
	}
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()

	user := *flagUsername
	if user == "" {
		user = os.Getenv("SCC_ORG_USERNAME")
	}
	pass := *flagPassword
	if pass == "" {
		pass = os.Getenv("SCC_ORG_PASSWORD")
	}
	if user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "error: org credentials required (--username/--password or SCC_ORG_USERNAME/SCC_ORG_PASSWORD)")
		flag.Usage()
		os.Exit(1)
	}

	baseURL := strings.TrimRight(*flagURL, "/")

	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println("  SCC org spike — Option 0 from PLAN.md")
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Printf("  Target:     %s\n", baseURL)
	fmt.Printf("  Org user:   %s\n", user)
	fmt.Printf("  Product:    %s / %s / %s\n", *flagProductID, *flagProductVer, *flagProductArch)
	fmt.Printf("  Proxy UUID: %s  (RMT header)\n", spikeProxyUUID)
	fmt.Printf("  Skip Q2+:   %v\n", *flagSkipQ2)
	fmt.Printf("  No cleanup: %v\n", *flagNoCleanup)

	client := newClient()

	// Q1 — always run; capture matched subscriptions for regcode use in Q2c.
	subs := q1Subscriptions(client, baseURL, user, pass, *flagProductID)

	if *flagSkipQ2 {
		fmt.Println("\n[Q2+ skipped via --skip-q2]")
		return
	}

	// Q0 — discover the correct product triplet + matching regcode.
	discoveredID, discoveredVer, discoveredArch, discoveredRegcode := qDiscoverProduct(client, baseURL, *flagProductID, *flagProductVer, *flagProductArch, subs)
	productID := *flagProductID
	productVer := *flagProductVer
	productArch := *flagProductArch
	if discoveredID != "" {
		productID = discoveredID
		productVer = discoveredVer
		productArch = discoveredArch
		fmt.Printf("  (overriding flags with discovered triplet: %s/%s/%s)\n", productID, productVer, productArch)
	}

	// Pick the regcode from a Rancher Prime subscription (RANCHER-X86 class).
	// Prefer a sub whose product_classes overlap with the discovered product class.
	var regcode string
	var regcodeSub orgSubscription
	for _, s := range subs {
		if s.RegCode == "" || s.Status != "ACTIVE" {
			continue
		}
		// Skip RKE-only subscriptions when we have a better match.
		hasRancherPrime := false
		for _, pc := range s.ProductClasses {
			if strings.Contains(strings.ToUpper(pc), "RANCHER-X86") {
				hasRancherPrime = true
			}
		}
		if hasRancherPrime || regcode == "" {
			regcode = s.RegCode
			regcodeSub = s
			if hasRancherPrime {
				break // prefer RANCHER-X86 over anything else
			}
		}
	}
	// If Q0 found a regcode paired with the matched product, prefer it.
	if discoveredRegcode != "" {
		regcode = discoveredRegcode
		fmt.Printf("  Regcode from Q0 product match: %q\n", regcode)
	} else if regcode != "" {
		fmt.Printf("  Regcode from Q1 heuristic: %q  (sub id=%d %q)\n", regcode, regcodeSub.ID, regcodeSub.Name)
	}

	// Q2 — create system via org POST
	q2SystemID, sysLogin, sysPass, q2ok := q2CreateSystem(
		client, baseURL, user, pass,
		productID, productVer, productArch,
	)

	// Verify — confirm that Q2 alone did NOT activate anything
	if q2ok {
		qVerify(client, baseURL, user, pass, sysLogin, sysPass, q2SystemID, subs)
	}

	// Q2c — actual activation via POST /connect/systems/products + regcode
	if q2ok && regcode != "" {
		q2cActivate(client, baseURL, sysLogin, sysPass, productID, productVer, productArch, regcode)
	} else if regcode == "" {
		fmt.Println("\n[Q2c skipped — no Rancher regcode found in Q1]")
	}

	// Q2b — bulk PUT path (informational; creates a second system)
	q2bSystemID := q2bBulkPUT(client, baseURL, user, pass, productID, productVer, productArch)

	// Q2d — announce via /connect/subscriptions/systems (regcode auth) + custom creds
	//       then immediately activate via /connect/systems/products (arch=unknown)
	var q2dSystemID int
	var q2dLogin, q2dPass string
	var q2dActivated bool
	if regcode != "" {
		q2dSystemID, q2dLogin, q2dPass, _ = q2dAnnounce(client, baseURL, regcode, productArch)
		if q2dSystemID != 0 {
			q2dActivated = q2dActivate(client, baseURL, q2dLogin, q2dPass, productID, productVer, productArch, regcode)
		}
	} else {
		fmt.Println("\n[Q2d skipped — no regcode available]")
	}

	// Q3 — verify system auth. Prefer the Q2d system when it activated, because
	// its /connect/systems/activations will be non-empty (proves the full loop).
	if q2dActivated {
		q3SystemAuth(client, baseURL, q2dLogin, q2dPass)
	} else if q2ok {
		q3SystemAuth(client, baseURL, sysLogin, sysPass)
	}

	// Cleanup. Two different delete paths by creation method:
	//   • Q2/Q2b (org POST/PUT)  → DELETE /organizations/systems/:id  (org creds)
	//   • Q2d   (regcode announce) → DELETE /connect/systems          (system creds)
	// The org endpoint returns 404 for announce-created systems.
	if *flagNoCleanup {
		if q2SystemID != 0 {
			fmt.Printf("\n  [no-cleanup] Q2  system id=%d  login=%s  left in SCC.\n", q2SystemID, sysLogin)
		}
		if q2bSystemID != 0 {
			fmt.Printf("  [no-cleanup] Q2b system id=%d  left in SCC.\n", q2bSystemID)
		}
		if q2dSystemID != 0 {
			fmt.Printf("  [no-cleanup] Q2d system id=%d  login=%s  left in SCC (deregister via DELETE /connect/systems).\n", q2dSystemID, q2dLogin)
		}
	} else {
		q4Delete(client, baseURL, user, pass, q2SystemID, "Q2")
		if q2bSystemID != 0 {
			q4Delete(client, baseURL, user, pass, q2bSystemID, "Q2b")
		}
		if q2dSystemID != 0 {
			q4DeregisterSystem(client, baseURL, q2dLogin, q2dPass, q2dSystemID)
		}
	}

	fmt.Println("\n══════════════════════════════════════════════════════════════")
	fmt.Println("  Spike complete. Cross-reference with PLAN.md §7.")
	fmt.Println("══════════════════════════════════════════════════════════════")
}
