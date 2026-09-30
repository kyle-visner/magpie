package magpie_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/kyle-visner/magpie"
)

func TestHostCanInvokeBookTools(t *testing.T) {
	store, err := magpie.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	book := magpie.NewBook(store, magpie.Context{Actor: magpie.ActorOwner})
	if !magpie.KnownTool("init") || !magpie.KnownTool("ledger_account_list") || magpie.KnownTool("nope") {
		t.Fatal("KnownTool does not match the catalog")
	}
	if len(magpie.ToolCatalog()) == 0 {
		t.Fatal("empty tool catalog")
	}
	if _, err := book.Invoke("init", nil); err != nil {
		t.Fatal(err)
	}
	out, err := book.Invoke("book_settings_get", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := magpie.EncodeJSON(out); err != nil {
		t.Fatal(err)
	}
	_, err = book.Invoke("book_settings_set", json.RawMessage(`{"accounting_basis":"sideways"}`))
	var appErr *magpie.AppError
	if !errors.As(err, &appErr) || appErr.Code != "validation_error" {
		t.Fatalf("want a validation AppError, got %v", err)
	}
}
