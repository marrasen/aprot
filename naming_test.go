package aprot

import "testing"

func TestToKebabAcronyms(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"Simple", "simple"},
		{"PublicHandlers", "public-handlers"},
		{"BulkXMLHandlers", "bulk-xml-handlers"},
		{"XMLParser", "xml-parser"},
		{"MyHTTPSServer", "my-https-server"},
		{"getUser", "get-user"},
		{"A", "a"},
		{"AB", "ab"},
		{"ABC", "abc"},
		{"ABCDef", "abc-def"},
		{"AWord", "a-word"},
		{"HTMLToJSON", "html-to-json"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := toKebabAcronyms(tt.input)
			if got != tt.want {
				t.Errorf("toKebabAcronyms(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestToLowerCamelAcronyms(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"CreateUser", "createUser"},
		{"XMLParser", "xmlParser"},
		{"HTTPSConnection", "httpsConnection"},
		{"A", "a"},
		{"AB", "ab"},
		{"ABC", "abc"},
		{"ABCDef", "abcDef"},
		{"Simple", "simple"},
		{"getUser", "getUser"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := toLowerCamelAcronyms(tt.input)
			if got != tt.want {
				t.Errorf("toLowerCamelAcronyms(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultNaming(t *testing.T) {
	t.Run("without FixAcronyms", func(t *testing.T) {
		n := DefaultNaming{}

		// FileName should match current toKebab behavior
		if got := n.FileName("PublicHandlers"); got != "public-handlers" {
			t.Errorf("FileName = %q, want %q", got, "public-handlers")
		}
		if got := n.FileName("BulkXMLHandlers"); got != "bulk-x-m-l-handlers" {
			t.Errorf("FileName = %q, want %q", got, "bulk-x-m-l-handlers")
		}

		// MethodName should match current toLowerCamel behavior
		if got := n.MethodName("CreateUser"); got != "createUser" {
			t.Errorf("MethodName = %q, want %q", got, "createUser")
		}
		if got := n.MethodName("XMLParser"); got != "xMLParser" {
			t.Errorf("MethodName = %q, want %q", got, "xMLParser")
		}

		// Prefixed names
		if got := n.HookName("CreateUser"); got != "useCreateUser" {
			t.Errorf("HookName = %q, want %q", got, "useCreateUser")
		}
		if got := n.HandlerName("UserCreated"); got != "onUserCreated" {
			t.Errorf("HandlerName = %q, want %q", got, "onUserCreated")
		}
		if got := n.ErrorMethodName("NotFound"); got != "isNotFound" {
			t.Errorf("ErrorMethodName = %q, want %q", got, "isNotFound")
		}
	})

	t.Run("with FixAcronyms", func(t *testing.T) {
		n := DefaultNaming{FixAcronyms: true}

		if got := n.FileName("BulkXMLHandlers"); got != "bulk-xml-handlers" {
			t.Errorf("FileName = %q, want %q", got, "bulk-xml-handlers")
		}
		if got := n.FileName("PublicHandlers"); got != "public-handlers" {
			t.Errorf("FileName = %q, want %q", got, "public-handlers")
		}

		if got := n.MethodName("XMLParser"); got != "xmlParser" {
			t.Errorf("MethodName = %q, want %q", got, "xmlParser")
		}
		if got := n.MethodName("CreateUser"); got != "createUser" {
			t.Errorf("MethodName = %q, want %q", got, "createUser")
		}
	})
}

func TestPreserveNaming(t *testing.T) {
	t.Run("without FixAcronyms", func(t *testing.T) {
		n := PreserveNaming{}

		// MethodName preserves PascalCase
		if got := n.MethodName("CreateUser"); got != "CreateUser" {
			t.Errorf("MethodName = %q, want %q", got, "CreateUser")
		}
		if got := n.MethodName("XMLParser"); got != "XMLParser" {
			t.Errorf("MethodName = %q, want %q", got, "XMLParser")
		}

		// FileName still kebab-cases
		if got := n.FileName("PublicHandlers"); got != "public-handlers" {
			t.Errorf("FileName = %q, want %q", got, "public-handlers")
		}

		// Prefixed names same as default
		if got := n.HookName("CreateUser"); got != "useCreateUser" {
			t.Errorf("HookName = %q, want %q", got, "useCreateUser")
		}
		if got := n.HandlerName("UserCreated"); got != "onUserCreated" {
			t.Errorf("HandlerName = %q, want %q", got, "onUserCreated")
		}
		if got := n.ErrorMethodName("NotFound"); got != "isNotFound" {
			t.Errorf("ErrorMethodName = %q, want %q", got, "isNotFound")
		}
	})

	t.Run("with FixAcronyms", func(t *testing.T) {
		n := PreserveNaming{FixAcronyms: true}

		// MethodName still preserves PascalCase
		if got := n.MethodName("XMLParser"); got != "XMLParser" {
			t.Errorf("MethodName = %q, want %q", got, "XMLParser")
		}

		// FileName uses acronym-aware kebab
		if got := n.FileName("BulkXMLHandlers"); got != "bulk-xml-handlers" {
			t.Errorf("FileName = %q, want %q", got, "bulk-xml-handlers")
		}
	})
}

func TestNamingPluginInterface(t *testing.T) {
	// Verify both types implement the interface
	var _ NamingPlugin = DefaultNaming{}
	var _ NamingPlugin = PreserveNaming{}
}
