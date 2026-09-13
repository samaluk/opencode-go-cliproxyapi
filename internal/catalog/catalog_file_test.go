package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogFileStartsWithoutHTTPAndRetainsExactMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(`{"data":[{"id":"glm-test","context_length":123456,"max_output_tokens":8192,"thinking":{"levels":["low","max"]},"input_modalities":["text","image"],"output_modalities":["text"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testCfg()
	cfg.CatalogFile = path
	fc := &fakeClient{}
	m := newManager(cfg, fc)
	mustRefresh(t, m)
	if fc.gotReq != nil {
		t.Fatal("file discovery must not issue HTTP requests")
	}
	record, ok := m.Lookup("opencode-go/glm-test")
	if !ok {
		t.Fatal("file model not routable")
	}
	if record.ContextLimit != 123456 || record.OutputLimit != 8192 {
		t.Fatalf("metadata lost: %+v", record)
	}
	if record.Thinking == nil || strings.Join(record.Thinking.Levels, ",") != "low,max" {
		t.Fatal("efforts changed")
	}
	if strings.Join(record.InputModes, ",") != "text,image" {
		t.Fatal("modalities changed")
	}
	if err := os.WriteFile(path, []byte(`invalid private content`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background(), testKey); err == nil || strings.Contains(err.Error(), "private content") {
		t.Fatalf("unsafe/missing parse error: %v", err)
	}
	if _, ok := m.Lookup("opencode-go/glm-test"); !ok {
		t.Fatal("last valid snapshot lost during file error")
	}
	if err := os.WriteFile(path, []byte(`{"data":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	mustRefresh(t, m)
	if _, ok := m.Lookup("opencode-go/glm-test"); ok {
		t.Fatal("valid empty file did not clear models")
	}
}

func TestCatalogFileErrorsHonorStalePolicy(t *testing.T) {
	cfg := testCfg()
	cfg.CatalogFile = filepath.Join(t.TempDir(), "sensitive-name.json")
	m := New(cfg, nil)
	err := m.Refresh(context.Background(), testKey)
	if err == nil || strings.Contains(err.Error(), cfg.CatalogFile) {
		t.Fatalf("file error includes path or is missing: %v", err)
	}
	if err := os.WriteFile(cfg.CatalogFile, []byte(`{"data":[{"id":"glm-test"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Catalog.StaleWhileUnavailable = false
	m = New(cfg, nil)
	mustRefresh(t, m)
	if err := os.Remove(cfg.CatalogFile); err != nil {
		t.Fatal(err)
	}
	if m.Refresh(context.Background(), testKey) == nil {
		t.Fatal("missing file accepted")
	}
	if _, ok := m.Lookup("opencode-go/glm-test"); ok {
		t.Fatal("stale model retained with stale policy disabled")
	}
}
