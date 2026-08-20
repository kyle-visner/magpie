package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"magpie/internal/magpie"
)

func TestMCPInitializeListsToolsAndPostsThroughBook(t *testing.T) {
	store, err := magpie.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := newMCPServer(magpie.NewBook(store, magpie.Context{Actor: "owner"}))

	initResp := mustHandle(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if initResp.Error != nil {
		t.Fatalf("initialize failed: %+v", initResp.Error)
	}
	result := asMap(t, initResp.Result)
	if result["protocolVersion"] != "2025-03-26" {
		t.Fatalf("unexpected protocol: %#v", result)
	}

	listResp := mustHandle(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := asMap(t, listResp.Result)["tools"].([]magpie.Tool)
	if len(tools) < 20 {
		t.Fatalf("expected full Magpie tool catalog, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, required := range []string{"init", "book_settings_get", "ledger_account_create", "ledger_journal_create", "invoice_import"} {
		if !names[required] {
			t.Fatalf("missing tool %s", required)
		}
	}

	call := func(id int, name, args string) map[string]any {
		t.Helper()
		if args == "" {
			args = "{}"
		}
		raw := mustHandle(t, server, `{"jsonrpc":"2.0","id":`+itoa(id)+`,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
		if raw.Error != nil {
			t.Fatalf("%s rpc error: %+v", name, raw.Error)
		}
		body := asMap(t, raw.Result)
		if body["isError"] == true {
			t.Fatalf("%s tool error: %#v", name, body)
		}
		return asMap(t, body["structuredContent"])
	}

	if call(3, "init", "")["root"] == nil {
		t.Fatal("init returned no root")
	}
	cash := call(4, "ledger_account_create", `{"name":"Checking","type":"asset","role":"operating_cash"}`)
	revenue := call(5, "ledger_account_create", `{"name":"Revenue","type":"revenue","role":"default_service_revenue"}`)
	cashID := asMap(t, cash["account"])["id"].(string)
	revenueID := asMap(t, revenue["account"])["id"].(string)
	journal := call(6, "ledger_journal_create", `{
		"date":"2026-08-19",
		"memo":"MCP sale",
		"manual_reason":"protocol test",
		"source":"mcp-test",
		"source_key":"sale-1",
		"postings":[
			{"account_id":"`+cashID+`","debit_cents":12000},
			{"account_id":"`+revenueID+`","credit_cents":12000}
		]
	}`)
	if asMap(t, journal["entry"])["id"] == "" {
		t.Fatalf("expected journal id, got %#v", journal)
	}

	dir := store.Dir()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cliOut := &bytes.Buffer{}
	if err := run([]string{"--store", dir, "ledger", "journal", "list"}, cliOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cliOut.String(), "MCP sale") {
		t.Fatalf("CLI should see the same journal the MCP path wrote:\n%s", cliOut.String())
	}
}

func TestMCPHTTPRequiresBearerAndServesHealth(t *testing.T) {
	store, err := magpie.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ts := httptest.NewServer(mcpHTTPHandler("secret-token", newMCPServer(magpie.NewBook(store, magpie.Context{Actor: "owner"}))))
	defer ts.Close()

	health, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health: %d", health.StatusCode)
	}

	unauth, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", unauth.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("authorized initialize: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Mcp-Session-Id") == "" {
		t.Fatal("expected session id on initialize")
	}
}

func TestMCPOpenerReleasesStoreSoCLICanInterleave(t *testing.T) {
	dir := t.TempDir()
	openCount := 0
	server := newMCPServerFromOpener(func() (*magpie.Book, func(), error) {
		openCount++
		store, err := magpie.OpenStore(dir)
		if err != nil {
			return nil, nil, err
		}
		return magpie.NewBook(store, magpie.Context{Actor: "owner"}), func() { _ = store.Close() }, nil
	})
	if resp := mustHandle(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"init","arguments":{}}}`); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "book", "settings", "get"}, &out); err != nil {
		t.Fatalf("CLI should run after MCP released the store: %v", err)
	}
	if openCount != 1 {
		t.Fatalf("expected one open/close cycle, got %d", openCount)
	}
}

func TestMCPStdioRoundTrip(t *testing.T) {
	store, err := magpie.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := serveMCPStdio(in, &out, newMCPServer(magpie.NewBook(store, magpie.Context{Actor: "owner"}))); err != nil {
		t.Fatal(err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("stdio tools/list: %+v", resp.Error)
	}
}

func mustHandle(t *testing.T, server *mcpServer, raw string) *rpcResponse {
	t.Helper()
	var req rpcRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	resp := server.handle(req)
	if resp == nil {
		t.Fatal("expected response")
	}
	return resp
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	if m, ok := value.(map[string]any); ok {
		return m
	}
	buf, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("not an object: %s", buf)
	}
	return out
}

func itoa(n int) string {
	return strings.TrimPrefix(strings.Replace(jsonNumber(n), "\n", "", 1), "")
}

func jsonNumber(n int) string {
	buf, _ := json.Marshal(n)
	return string(buf)
}
