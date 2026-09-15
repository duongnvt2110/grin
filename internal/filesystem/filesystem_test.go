package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grin/internal/config"
	"grin/internal/errs"
)

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	limits := config.Defaults().Limits
	service, err := New(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	return service, root
}

func TestRedactSensitiveContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "password", input: "PASSWORD=secret", want: "PASSWORD=<redacted>"},
		{name: "spacing", input: "PASSWORD = secret", want: "PASSWORD = <redacted>"},
		{name: "quoted json", input: `"api_key": "secret",`, want: `"api_key": "<redacted>",`},
		{name: "camel secret", input: "clientSecret=secret", want: "clientSecret=<redacted>"},
		{name: "refresh token", input: "refresh_token=secret", want: "refresh_token=<redacted>"},
		{name: "provider key", input: "OPENAI_API_KEY=secret", want: "OPENAI_API_KEY=<redacted>"},
		{name: "token metadata", input: "TOKEN_LIMIT=8192", want: "TOKEN_LIMIT=8192"},
		{name: "password metadata", input: "PASSWORD_POLICY=min12", want: "PASSWORD_POLICY=min12"},
		{name: "author", input: "AUTHOR=alice", want: "AUTHOR=alice"},
		{name: "public key", input: "public_key=value", want: "public_key=value"},
		{name: "conservative assignment", input: "password = get_password_from_vault()", want: "password = <redacted>"},
		{name: "go short declaration", input: `password := "test"`, want: `password := "test"`},
		{name: "url credentials", input: "DATABASE_URL=postgres://user:pass@host/db", want: "DATABASE_URL=postgres://<redacted>@host/db"},
		{name: "url in short declaration", input: `databaseURL := "postgres://user:pass@host"`, want: `databaseURL := "postgres://<redacted>@host"`},
		{name: "path at", input: "https://example.com/path@version", want: "https://example.com/path@version"},
		{name: "query at", input: "https://example.com?email=user@example.com", want: "https://example.com?email=user@example.com"},
		{name: "later url", input: `urls := "https://public.example postgres://user:pass@db/app"`, want: `urls := "https://public.example postgres://<redacted>@db/app"`},
		{name: "missing host", input: "proto://user:pass@", want: "proto://user:pass@"},
		{name: "missing user", input: "proto://:pass@host", want: "proto://:pass@host"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := redactSensitiveContent(test.input); got != test.want {
				t.Fatalf("redactSensitiveContent() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadTextRedactsSensitiveContent(t *testing.T) {
	service, root := testService(t)
	const secret = "GRIN_TEST_SECRET_7F91C"
	content := "PASSWORD=" + secret + "\nDATABASE_URL=postgres://user:urlsecret@host/db\nPORT=3306\n"
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadText(context.Background(), ReadInput{Path: "secret.txt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, secret) || strings.Contains(result.Content, "urlsecret") {
		t.Fatal("ReadText returned unredacted sensitive content")
	}
	if !strings.Contains(result.Content, "PASSWORD=<redacted>") || !strings.Contains(result.Content, "postgres://<redacted>@host/db") || !strings.Contains(result.Content, "PORT=3306") {
		t.Fatalf("unexpected redacted content: %q", result.Content)
	}
	if result.Bytes != int64(len(content)) {
		t.Fatalf("ReadText bytes = %d, want %d", result.Bytes, len(content))
	}
}

func TestReadTextRejectsOutsideWorkspaceAndEnforcesLimit(t *testing.T) {
	service, root := testService(t)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := service.ReadText(context.Background(), ReadInput{Path: "note.txt", MaxBytes: 4}, false)
	var typed errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.ErrOutputLimit {
		t.Fatalf("expected output limit, got %v", err)
	}
	_, err = service.ReadText(context.Background(), ReadInput{Path: "../outside.txt"}, false)
	if !errors.As(err, &typed) || typed.Code != errs.ErrNotFound {
		t.Fatalf("expected not found for relative escape, got %v", err)
	}
	result, err := service.ReadText(context.Background(), ReadInput{Path: filepath.Join(root, "note.txt")}, false)
	if err != nil || result.Path != "note.txt" || result.Content != "hello" {
		t.Fatalf("absolute workspace read = %+v, err=%v", result, err)
	}
}

func TestListDoesNotFollowSymlinkDirectory(t *testing.T) {
	service, root := testService(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result, err := service.List(context.Background(), ListInput{Path: ".", MaxEntries: 10}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Type != "symlink" {
		t.Fatalf("unexpected entries: %+v", result.Entries)
	}
}

func TestWriteTextIsBoundedAndAtomic(t *testing.T) {
	service, root := testService(t)
	result, err := service.WriteText(context.Background(), WriteInput{Path: "note.txt", Content: "first"}, false)
	if err != nil || result.Path != "note.txt" || result.Bytes != 5 {
		t.Fatalf("write result = %+v, err=%v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(data) != "first" {
		t.Fatalf("written data = %q, err=%v", data, err)
	}
	_, err = service.WriteText(context.Background(), WriteInput{Path: "note.txt", Content: "second"}, false)
	var typed errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.ErrFileExists {
		t.Fatalf("expected file-exists error, got %v", err)
	}
	if _, err := service.WriteText(context.Background(), WriteInput{Path: "note.txt", Content: "second", Overwrite: true}, false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(data) != "second" {
		t.Fatalf("overwritten data = %q, err=%v", data, err)
	}

	absolutePath := filepath.Join(root, "absolute.txt")
	result, err = service.WriteText(context.Background(), WriteInput{Path: absolutePath, Content: "absolute"}, false)
	if err != nil || result.Path != "absolute.txt" || result.Bytes != 8 {
		t.Fatalf("absolute workspace write = %+v, err=%v", result, err)
	}
	data, err = os.ReadFile(absolutePath)
	if err != nil || string(data) != "absolute" {
		t.Fatalf("absolute written data = %q, err=%v", data, err)
	}

	outside := filepath.Join(t.TempDir(), "missing", "outside.txt")
	_, err = service.WriteText(context.Background(), WriteInput{Path: outside, Content: "no"}, false)
	if !errors.As(err, &typed) || typed.Code != errs.ErrOutsideWorkspace {
		t.Fatalf("expected outside-workspace for absolute path, got %v", err)
	}
}

func TestWriteTextRejectsOutsideAndSymlinkParent(t *testing.T) {
	service, root := testService(t)
	_, err := service.WriteText(context.Background(), WriteInput{Path: "../outside.txt", Content: "no"}, false)
	var typed errs.Error
	if !errors.As(err, &typed) || typed.Code != errs.ErrOutsideWorkspace {
		t.Fatalf("expected outside-workspace error, got %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err = service.WriteText(context.Background(), WriteInput{Path: "linked/secret.txt", Content: "no"}, false)
	if !errors.As(err, &typed) || typed.Code != errs.ErrOutsideWorkspace {
		t.Fatalf("expected symlink escape error, got %v", err)
	}
}

func TestEditTextReplacesOnceAndPreservesMode(t *testing.T) {
	service, root := testService(t)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := service.EditText(context.Background(), EditTextInput{Path: "note.txt", OldText: "hello", NewText: "goodbye"}, false)
	if err != nil || result.Path != "note.txt" || result.Bytes != int64(len("goodbye world")) {
		t.Fatalf("edit result = %+v, err=%v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "goodbye world" {
		t.Fatalf("edited data = %q, err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 640", got)
	}
}

func TestEditTextRejectsMissingAndAmbiguousMatchesWithoutChangingFile(t *testing.T) {
	service, root := testService(t)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("one two one"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, input := range []EditTextInput{
		{Path: "note.txt", OldText: "missing", NewText: "x"},
		{Path: "note.txt", OldText: "one", NewText: "x"},
	} {
		_, err := service.EditText(context.Background(), input, false)
		var typed errs.Error
		if !errors.As(err, &typed) {
			t.Fatalf("EditText(%+v) error = %v", input, err)
		}
		want := errs.ErrNotFound
		if input.OldText == "one" {
			want = errs.ErrInvalidInput
		}
		if typed.Code != want {
			t.Fatalf("EditText(%+v) code = %s, want %s", input, typed.Code, want)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("file changed after failed edits: %q, err=%v", after, err)
	}
}

func TestValidateEditRejectsImpossibleTextSizes(t *testing.T) {
	root := t.TempDir()
	limits := config.Defaults().Limits
	limits.MaxFileReadBytes = 4
	limits.MaxFileWriteBytes = 5
	service, err := New(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []EditTextInput{
		{Path: "note.txt", OldText: "12345", NewText: "x"},
		{Path: "note.txt", OldText: "1", NewText: "123456"},
	} {
		if err := service.ValidateEdit(input); err == nil {
			t.Fatalf("ValidateEdit(%+v) unexpectedly succeeded", input)
		} else {
			var typed errs.Error
			if !errors.As(err, &typed) || typed.Code != errs.ErrOutputLimit {
				t.Fatalf("ValidateEdit(%+v) error = %v, want output_limit", input, err)
			}
		}
	}
}

func TestClassifyOutsidePathsAndExecuteWhenAllowed(t *testing.T) {
	service, root := testService(t)
	outside := t.TempDir()
	path := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(path, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	readOutside, err := service.ClassifyExisting(path)
	if err != nil || !readOutside {
		t.Fatalf("ClassifyExisting(%q) = %v, err=%v", path, readOutside, err)
	}
	if _, err := service.ReadText(context.Background(), ReadInput{Path: path}, true); err != nil {
		t.Fatalf("authorized outside read: %v", err)
	}

	writePath := filepath.Join(outside, "new.txt")
	writeOutside, err := service.ClassifyCreate(writePath)
	if err != nil || !writeOutside {
		t.Fatalf("ClassifyCreate(%q) = %v, err=%v", writePath, writeOutside, err)
	}
	if err := service.ValidateWrite(WriteInput{Path: writePath, Content: "created"}); err != nil {
		t.Fatalf("input-only outside validation: %v", err)
	}
	if _, err := service.WriteText(context.Background(), WriteInput{Path: writePath, Content: "created"}, true); err != nil {
		t.Fatalf("authorized outside write: %v", err)
	}

	if outside, err := service.ClassifyExisting(""); err != nil || outside {
		t.Fatalf("ClassifyExisting(empty) = %v, err=%v", outside, err)
	}
	insideMissing := filepath.Join(root, "missing", "child")
	if outside, err := service.ClassifyExisting(insideMissing); err != nil || outside {
		t.Fatalf("ClassifyExisting(%q) = %v, err=%v", insideMissing, outside, err)
	}
	if outside, err := service.ClassifyCreate(insideMissing); err != nil || outside {
		t.Fatalf("ClassifyCreate(%q) = %v, err=%v", insideMissing, outside, err)
	}

	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if outside, err := service.ClassifyExisting(filepath.Join(link, "note.txt")); err != nil || !outside {
		t.Fatalf("ClassifyExisting(symlink) = %v, err=%v", outside, err)
	}
	if outside, err := service.ClassifyCreate(filepath.Join(link, "new.txt")); err != nil || !outside {
		t.Fatalf("ClassifyCreate(symlink) = %v, err=%v", outside, err)
	}
}

func TestYoloAllowsOutsideWorkspaceFilesystemOperations(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, config.Defaults().Limits, true)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, ok := service.Info()["yolo"].(bool); !ok || !enabled {
		t.Fatalf("workspace info yolo = %#v", service.Info()["yolo"])
	}
	if mode := service.Info()["mode"]; mode != "yolo" {
		t.Fatalf("workspace info mode = %#v", mode)
	}
	if _, ok := service.Info()["read_only"]; ok {
		t.Fatalf("workspace info still exposes removed read_only field: %#v", service.Info())
	}
	path := filepath.Join(outside, "note.txt")
	if _, err := service.ReadText(context.Background(), ReadInput{Path: path}, false); err != nil {
		t.Fatalf("YOLO read outside workspace: %v", err)
	}
	if _, err := service.Stat(context.Background(), StatInput{Path: path}, false); err != nil {
		t.Fatalf("YOLO stat outside workspace: %v", err)
	}
	if _, err := service.List(context.Background(), ListInput{Path: outside}, false); err != nil {
		t.Fatalf("YOLO list outside workspace: %v", err)
	}
	if _, err := service.Search(context.Background(), SearchInput{Path: outside, Query: "hello"}, false); err != nil {
		t.Fatalf("YOLO search outside workspace: %v", err)
	}
	if _, err := service.WriteText(context.Background(), WriteInput{Path: filepath.Join(outside, "new.txt"), Content: "new"}, false); err != nil {
		t.Fatalf("YOLO write outside workspace: %v", err)
	}
}

func TestSearchFindsTextAndFileNames(t *testing.T) {
	service, root := testService(t)
	if err := os.WriteFile(filepath.Join(root, "FIXME.txt"), []byte("first\nFIXME second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), SearchInput{Query: "FIXME", MaxResults: 10}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || result.Matches[0].Path != "FIXME.txt" {
		t.Fatalf("unexpected matches: %+v", result.Matches)
	}
}
