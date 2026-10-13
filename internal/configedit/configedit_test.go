package configedit

import (
	"testing"
)

func TestPropertiesRoundTrip(t *testing.T) {
	src := "#Mon Jan 01\nmotd=Hello World\nserver-port=25565\nonline-mode=true\nview-distance=10\n"
	doc := ParseProperties(src)
	if doc.Values["motd"] != "Hello World" || doc.Values["server-port"] != "25565" {
		t.Fatalf("parse wrong: %v", doc.Values)
	}
	out := RenderProperties(doc)
	doc2 := ParseProperties(out)
	for k, v := range doc.Values {
		if doc2.Values[k] != v {
			t.Fatalf("round trip mismatch for %s: %q != %q", k, doc2.Values[k], v)
		}
	}
}

func TestPropertiesTypes(t *testing.T) {
	doc := ParseProperties("a=true\nb=42\nc=1.5\nd=hello")
	f := doc.ToForm()
	types := map[string]string{}
	for _, ff := range f {
		types[ff.Key] = ff.Type
	}
	if types["a"] != "boolean" || types["b"] != "integer" || types["c"] != "float" || types["d"] != "string" {
		t.Fatalf("type inference wrong: %v", types)
	}
}

func TestVisualFormMerge(t *testing.T) {
	doc := ParseProperties("motd=Hi\nserver-port=25565\nonline-mode=true\nunknown-key=1")
	form := BuildVisualForm(MCPropertiesSchema(), doc)
	sections := form["sections"].([]map[string]interface{})
	if len(sections) < 2 {
		t.Fatalf("expected sections, got %d", len(sections))
	}
	found := 0
	for _, sec := range sections {
		if sec["id"] == "other" {
			fields := sec["fields"].([]FormField)
			if len(fields) != 1 || fields[0].Key != "unknown-key" {
				t.Fatal("other section wrong")
			}
		} else {
			found += len(sec["fields"].([]FormField))
		}
	}
	if found == 0 {
		t.Fatal("no schema fields matched")
	}
	// apply visual edit
	ApplyVisualForm(doc, map[string]string{"motd": "Changed"})
	if doc.Values["motd"] != "Changed" {
		t.Fatal("visual apply failed")
	}
}

func TestJSONValidation(t *testing.T) {
	if err := ValidateJSON([]byte(`{"a": 1}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJSON([]byte(`{broken`)); err == nil {
		t.Fatal("invalid json accepted")
	}
	formatted, err := FormatJSON([]byte(`{"a":1,"b":[1,2]}`))
	if err != nil || len(formatted) == 0 {
		t.Fatalf("format: %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	r := NewRegistry()
	if _, err := r.ParseManifest([]byte(`{"name":"Paper 1.21","type":"minecraft-java","create_type":"download_jar","jar_url":"https://x/paper.jar","port":25565}`)); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if _, err := r.ParseManifest([]byte(`{"name":"Broken","create_type":"download_jar"}`)); err == nil {
		t.Fatal("jar_url required")
	}
	if _, err := r.ParseManifest([]byte(`not json`)); err == nil {
		t.Fatal("bad json accepted")
	}
}

func TestManifestNormalizationAndSafety(t *testing.T) {
	r := NewRegistry()
	m, err := r.ParseManifest([]byte(`{"name":"Paper 1.21","jar_url":"https://downloads.example/paper.jar"}`))
	if err != nil {
		t.Fatalf("infer valid download recipe: %v", err)
	}
	if m.ID != "paper-1-21" || m.Type != "minecraft-java" || m.CreateType != "download_jar" || m.Version != "1" {
		t.Fatalf("normalized manifest = %+v", m)
	}

	invalid := []string{
		`{"name":"Bad URL","create_type":"download_jar","jar_url":"file:///etc/passwd"}`,
		`{"name":"Missing raw fields","create_type":"raw_exec"}`,
		`{"name":"Missing archive URL","create_type":"import_zip"}`,
		`{"name":"Traversal","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh","files":{"../../outside":"x"}}`,
		`{"name":"Duplicate normalized paths","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh","files":{"config//server.ini":"a","config/server.ini":"b"}}`,
		`{"name":"Bad ID","id":"../escape","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh"}`,
		`{"name":"Bad ID ending","id":"bad-","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh"}`,
		`{"name":"Unknown key","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh","exectuion_command":"typo"}`,
		`{"name":"Property injection","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh","config":{"motd":"hello\nserver-port=1"}}`,
		`{"name":"Property key injection","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh","config":{"motd\nserver-port":"1"}}`,
		`{"name":"Embedded URL credentials","create_type":"download_jar","jar_url":"https://user:secret@downloads.example/server.jar"}`,
		`{"name":"App ID overflow","create_type":"steam_game","type":"steam_cmd","app_id":"2147483648"}`,
		`{"name":"App ID zero","create_type":"steam_game","type":"steam_cmd","app_id":"0"}`,
		`{"name":"App ID sign","create_type":"steam_game","type":"steam_cmd","app_id":"+123"}`,
		`{"name":"Options on raw recipe","create_type":"raw_exec","executable":"run.sh","execution_command":"./run.sh","steam_options":{"password":"secret"}}`,
	}
	for _, raw := range invalid {
		if _, err := r.ParseManifest([]byte(raw)); err == nil {
			t.Errorf("unsafe or incomplete manifest accepted: %s", raw)
		}
	}

	steamRecipe, err := r.ParseManifest([]byte(`{"name":"Valheim","create_type":"steam_game","type":"steam_cmd","app_id":"896660","steam_options":{"password":"not-a-real-secret"}}`))
	if err != nil || steamRecipe.SteamOptions["password"] != "not-a-real-secret" {
		t.Fatalf("steam recipe = %+v, err=%v", steamRecipe, err)
	}

	zipManifest, err := r.ParseManifest([]byte(`{"name":"Imported","archive_url":"https://downloads.example/server.zip"}`))
	if err != nil || zipManifest.CreateType != "import_zip" {
		t.Fatalf("zip manifest = %+v, err=%v", zipManifest, err)
	}
}
