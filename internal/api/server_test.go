package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jvzantvoort/gogalaxy/internal/api"
	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/service"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
	_ "github.com/jvzantvoort/gogalaxy/internal/storage/file"
	"github.com/jvzantvoort/gogalaxy/internal/testutil"
)

type env struct {
	t      *testing.T
	srv    *httptest.Server
	svc    *service.Service
	tokens map[string]string
}

func newEnv(t *testing.T, settings map[string]any) *env {
	t.Helper()
	v := viper.New()
	config.SetDefaults(v)
	v.Set("storage.path", t.TempDir())
	for k, val := range settings {
		v.Set(k, val)
	}
	cfg, err := config.Load(v)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(cfg, store, nil, log)
	chain, err := auth.Build(cfg.Auth.Backends, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewHandler(api.Options{Service: svc, Auth: chain, Log: log}))
	t.Cleanup(srv.Close)

	e := &env{t: t, srv: srv, svc: svc, tokens: map[string]string{}}
	ctx := context.Background()
	for _, u := range []service.NewUser{{Username: "admin", Password: "pw", Superuser: true}, {Username: "alice"}, {Username: "bob"}} {
		if _, err := svc.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if e.tokens[u.Username], err = svc.IssueToken(ctx, u.Username); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.CreateNamespace(ctx, service.System, "testns", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) do(user, method, path string, body io.Reader, contentType string) (int, map[string]any) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("Authorization", "Token "+e.tokens[user])
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (e *env) get(user, path string) (int, map[string]any) {
	return e.do(user, http.MethodGet, path, nil, "")
}

// publish uploads like ansible-galaxy does: base64 encoded file part.
func (e *env) publish(user, filename string, data []byte) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	sum := sha256.Sum256(data)
	_ = mw.WriteField("sha256", hex.EncodeToString(sum[:]))
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Transfer-Encoding", "base64")
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	part, err := mw.CreatePart(h)
	if err != nil {
		e.t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		fmt.Fprintf(part, "%s\r\n", enc[:76])
		enc = enc[76:]
	}
	fmt.Fprint(part, enc)
	mw.Close()
	return e.do(user, http.MethodPost, "/api/galaxy/v3/artifacts/collections/", &buf, mw.FormDataContentType())
}

func TestRoot(t *testing.T) {
	e := newEnv(t, nil)
	code, body := e.get("", "/api/galaxy/")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	av := body["available_versions"].(map[string]any)
	if av["v3"] != "v3/" || av["v1"] != "v1/" {
		t.Fatalf("available_versions = %v", av)
	}
	if _, ok := body["server_version"]; ok {
		t.Fatal("server_version must not be shown to anonymous users")
	}
	if code, body = e.get("alice", "/api/galaxy/content/published/"); code != 200 || body["distro_base_path"] != "published" {
		t.Fatalf("content root: %d %v", code, body)
	}
	if code, _ = e.get("", "/api/galaxy/content/nope/"); code != 404 {
		t.Fatalf("unknown repo: %d", code)
	}
}

func TestCollectionLifecycle(t *testing.T) {
	e := newEnv(t, nil)
	art := testutil.Collection(t, "testns", "demo", "1.0.0", map[string]string{"other.dep": ">=1.0.0"})

	if code, _ := e.publish("", "testns-demo-1.0.0.tar.gz", art); code != 401 {
		t.Fatalf("anonymous publish: %d", code)
	}
	if code, _ := e.publish("bob", "testns-demo-1.0.0.tar.gz", art); code != 403 {
		t.Fatalf("non-owner publish: %d", code)
	}
	code, body := e.publish("alice", "testns-demo-1.0.0.tar.gz", art)
	if code != 202 {
		t.Fatalf("publish: %d %v", code, body)
	}
	task := body["task"].(string)
	if code, body = e.get("alice", task); code != 200 || body["state"] != "completed" || body["finished_at"] == nil {
		t.Fatalf("task: %d %v", code, body)
	}
	if code, _ = e.publish("alice", "testns-demo-1.0.0.tar.gz", art); code != 400 {
		t.Fatalf("duplicate publish: %d", code)
	}
	if code, _ = e.publish("alice", "testns-demo-2.0.0.tar.gz", art); code != 202 {
		t.Fatalf("mismatching filename should produce a failed task: %d", code)
	}
	if code, _ := e.publish("alice", "testns-demo-1.1.0-beta.1.tar.gz", testutil.Collection(t, "testns", "demo", "1.1.0-beta.1", nil)); code != 202 {
		t.Fatalf("publish prerelease: %d", code)
	}

	for _, base := range []string{"/api/galaxy/v3", "/api/galaxy/content/published/v3", "/api/galaxy/v3/plugin/ansible/content/published/collections/index"} {
		code, body = e.get("", base+"/collections/testns/demo/")
		if code != 200 {
			t.Fatalf("%s collection: %d %v", base, code, body)
		}
		if hv := body["highest_version"].(map[string]any); hv["version"] != "1.0.0" {
			t.Fatalf("%s highest_version = %v (pre-releases must not win)", base, hv)
		}
		code, body = e.get("", base+"/collections/testns/demo/versions/?limit=1")
		if code != 200 || body["meta"].(map[string]any)["count"] != 2.0 || len(body["data"].([]any)) != 1 {
			t.Fatalf("%s versions: %d %v", base, code, body)
		}
		if next := body["links"].(map[string]any)["next"].(string); !strings.HasPrefix(next, base+"/collections/testns/demo/versions/?") {
			t.Fatalf("next link %q must be an absolute path", next)
		}
	}

	code, body = e.get("", "/api/galaxy/v3/collections/testns/demo/versions/1.0.0/")
	if code != 200 {
		t.Fatalf("version: %d", code)
	}
	if body["namespace"].(map[string]any)["name"] != "testns" || body["collection"].(map[string]any)["name"] != "demo" {
		t.Fatalf("version detail: %v", body)
	}
	if deps := body["metadata"].(map[string]any)["dependencies"].(map[string]any); deps["other.dep"] != ">=1.0.0" {
		t.Fatalf("dependencies: %v", deps)
	}
	sum := sha256.Sum256(art)
	if body["artifact"].(map[string]any)["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatal("artifact sha256 mismatch")
	}
	resp, err := http.Get(body["download_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, art) {
		t.Fatalf("download: %d, %d bytes", resp.StatusCode, len(got))
	}

	if code, body = e.get("", "/api/galaxy/v3/plugin/ansible/search/collection-versions/?is_highest=true"); code != 200 ||
		body["meta"].(map[string]any)["count"] != 1.0 {
		t.Fatalf("search: %d %v", code, body)
	}

	if code, _ = e.do("bob", http.MethodDelete, "/api/galaxy/v3/collections/testns/demo/versions/1.0.0/", nil, ""); code != 403 {
		t.Fatalf("non-owner delete: %d", code)
	}
	if code, _ = e.do("alice", http.MethodDelete, "/api/galaxy/v3/collections/testns/demo/", nil, ""); code != 202 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ = e.get("", "/api/galaxy/v3/collections/testns/demo/"); code != 404 {
		t.Fatalf("deleted collection: %d", code)
	}
}

func TestAnonymousReadDisabled(t *testing.T) {
	e := newEnv(t, map[string]any{"auth.anonymous_read": false})
	if code, _ := e.get("", "/api/galaxy/v3/collections/"); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := e.get("bob", "/api/galaxy/v3/collections/"); code != 200 {
		t.Fatalf("authenticated: %d", code)
	}
	if code, _ := e.get("", "/api/galaxy/"); code != 200 {
		t.Fatalf("API root must stay public: %d", code)
	}
}

func TestInvalidCredentials(t *testing.T) {
	e := newEnv(t, nil)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/galaxy/v3/collections/", nil)
	req.SetBasicAuth("admin", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("status %d, challenge %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	req, _ = http.NewRequest(http.MethodPost, e.srv.URL+"/api/galaxy/v3/auth/token/", nil)
	req.SetBasicAuth("admin", "pw")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if len(tok["token"]) != 40 {
		t.Fatalf("token response %v", tok)
	}
}

func TestRoles(t *testing.T) {
	for _, anon := range []bool{true, false} {
		t.Run(fmt.Sprintf("anonymous_read=%t", anon), func(t *testing.T) {
			e := newEnv(t, map[string]any{"auth.anonymous_read": anon})
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			_ = mw.WriteField("namespace", "testns")
			_ = mw.WriteField("name", "web")
			_ = mw.WriteField("version", "1.0.0")
			fw, _ := mw.CreateFormFile("file", "web.tar.gz")
			archive := testutil.Role(t, "web-1.0.0")
			_, _ = fw.Write(archive)
			mw.Close()
			if code, body := e.do("alice", http.MethodPost, "/api/galaxy/v1/imports/", &buf, mw.FormDataContentType()); code != 200 {
				t.Fatalf("import: %d %v", code, body)
			}

			code, body := e.get("alice", "/api/galaxy/v1/roles/?owner__username=testns&name=web")
			results := body["results"].([]any)
			if code != 200 || len(results) != 1 {
				t.Fatalf("lookup: %d %v", code, body)
			}
			role := results[0].(map[string]any)
			id := int(role["id"].(float64))

			code, body = e.get("alice", fmt.Sprintf("/api/galaxy/v1/roles/%d/versions/", id))
			if code != 200 || body["next_link"] != nil {
				t.Fatalf("versions: %d %v", code, body)
			}
			v := body["results"].([]any)[0].(map[string]any)
			if v["name"] != "1.0.0" {
				t.Fatalf("version: %v", v)
			}
			// ansible-galaxy downloads roles without credentials.
			resp, err := http.Get(v["download_url"].(string))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || !bytes.Equal(got, archive) {
				t.Fatalf("download: %d", resp.StatusCode)
			}
			if !anon {
				resp, err := http.Get(e.srv.URL + "/api/galaxy/v1/artifacts/roles/testns-web-1.0.0.tar.gz")
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 401 {
					t.Fatalf("unsigned download: %d", resp.StatusCode)
				}
			}

			if code, body = e.do("bob", http.MethodDelete, "/api/galaxy/v1/removerole/?github_user=testns&github_repo=web", nil, ""); code != 403 {
				t.Fatalf("non-owner remove: %d %v", code, body)
			}
			code, body = e.do("alice", http.MethodDelete, "/api/galaxy/v1/removerole/?github_user=testns&github_repo=web", nil, "")
			if code != 200 || len(body["deleted_roles"].([]any)) != 1 {
				t.Fatalf("remove: %d %v", code, body)
			}
		})
	}
}

func TestExecutionEnvironments(t *testing.T) {
	e := newEnv(t, nil)
	code, body := e.get("", "/api/galaxy/v3/plugin/execution-environments/repositories/")
	if code != 200 || body["meta"].(map[string]any)["count"] != 0.0 {
		t.Fatalf("list: %d %v", code, body)
	}
	code, body = e.get("", "/api/galaxy/v3/plugin/execution-environments/repositories/ee-minimal/")
	if code != 404 || !strings.Contains(fmt.Sprint(body), "No execution environments") {
		t.Fatalf("detail: %d %v", code, body)
	}
	code, body = e.get("", "/v2/ee-minimal/manifests/latest")
	if code != 404 || !strings.Contains(fmt.Sprint(body), "NAME_UNKNOWN") {
		t.Fatalf("registry: %d %v", code, body)
	}
}
