package aprot

import (
	"bytes"
	"encoding"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// goClientPkgPath is the import path of the hand-written Go runtime the
// generated code calls into.
const goClientPkgPath = "github.com/marrasen/aprot/client"

// goClientFileName is the single file the Go generator writes.
const goClientFileName = "client.gen.go"

// aprotPkgPath is the import path of this package. Generated Go code must
// never import it: a client must not drag in the server.
var aprotPkgPath = reflect.TypeFor[Registry]().PkgPath()

// goPromotedClientMembers are the exported methods the generated Client
// promotes from the embedded *client.Client, plus the embedded field itself.
// A handler group whose field name equals one of these would shadow it, so
// generation fails instead. A test in generate_go_test.go keeps this list in
// step with the runtime package.
var goPromotedClientMembers = []string{"Client", "Close", "Done", "Err", "RefreshAuth", "State"}

var (
	jsonMarshalerToType = reflect.TypeFor[jsonv2.MarshalerTo]()
	textMarshalerIface  = reflect.TypeFor[encoding.TextMarshaler]()
	textAppenderIface   = reflect.TypeFor[encoding.TextAppender]()
)

// GoGeneratorOptions configures [GoGenerator].
type GoGeneratorOptions struct {
	// OutputDir is the directory the generated package is written to. If
	// empty, Generate only returns the file contents.
	OutputDir string

	// PackageName is the package clause of the generated code. Defaults to
	// the base name of OutputDir, or "api" when that is not a valid
	// identifier.
	PackageName string

	// ImportTypes lists package import paths whose types the generated code
	// references directly, with an import, instead of copying their wire
	// shape. Use it for third-party value types the client already depends
	// on, such as "github.com/google/uuid".
	//
	// A listed package must not import the server, directly or through its
	// own imports: the generated client imports it, and so would pull the
	// server in. The generator only refuses the root aprot package itself;
	// it cannot see what a listed package imports.
	//
	// A listed type must not hold a database/sql Null* value at any depth.
	// The server flattens those to value-or-null, and the client's decoder
	// does not, so generation fails for such a type.
	ImportTypes []string
}

// GoGenerator generates a typed Go client package from a registry. The
// generated code calls the runtime package github.com/marrasen/aprot/client
// and declares its own copies of every type it needs, so it never imports
// the server's packages.
type GoGenerator struct {
	registry *Registry
	options  GoGeneratorOptions

	importTypes map[string]bool

	// named holds every named type the generated package declares, keyed by
	// its server type. order is the discovery order.
	named map[reflect.Type]*goNamedType
	order []reflect.Type

	// rendering is false during the collection pass, which discovers types
	// and imports, and true during the pass that produces the final source.
	rendering bool

	// importNames maps an import path to its local name. Collected during
	// the first pass, finalized before the second.
	importNames map[string]string
	usedImports map[string]bool
	// realPkgNames records the package clause of imported non-standard
	// packages, read from reflect (uuid.UUID → "uuid").
	realPkgNames map[string]string

	// meta holds source metadata (docs, parameter names) per package path.
	meta map[string]*goSourceMeta

	// topLevel holds every package-level identifier of the generated code,
	// set before rendering, so a parameter never shadows one.
	topLevel map[string]bool

	// importChecked records the ImportTypes types already checked for
	// sql.Null fields.
	importChecked map[reflect.Type]bool

	errs []error
}

// goNamedType is a type the generated package declares.
type goNamedType struct {
	t    reflect.Type
	name string // final name, set before rendering
	enum *EnumInfo
	// marshaled holds the members' JSON string values when the enum type
	// has its own marshaler that encodes them as strings ("High" for a
	// Stringer int enum with MarshalText). The type is then declared as a
	// string type with these values.
	marshaled []string
	// level selects the name candidate during assignNames.
	level int
}

// goSourceMeta is the source metadata of one package: the TypeScript
// generator's sourceMeta plus docs for non-struct type declarations, which
// sourceMeta does not record.
type goSourceMeta struct {
	*sourceMeta
	typeDocs map[string]string
}

// NewGoGenerator creates a Go client generator for registry.
func NewGoGenerator(registry *Registry) *GoGenerator {
	return &GoGenerator{registry: registry}
}

// WithOptions sets the generator options.
func (g *GoGenerator) WithOptions(opts GoGeneratorOptions) *GoGenerator {
	g.options = opts
	return g
}

func (g *GoGenerator) errorf(format string, args ...any) {
	g.errs = append(g.errs, fmt.Errorf(format, args...))
}

// Generate produces the Go client. It returns a map of file name to content
// and, when OutputDir is set, writes the files there and removes .go files a
// previous run generated that this run did not produce.
func (g *GoGenerator) Generate() (map[string]string, error) {
	pkgName, err := g.packageName()
	if err != nil {
		return nil, err
	}
	g.importTypes = make(map[string]bool, len(g.options.ImportTypes))
	for _, p := range g.options.ImportTypes {
		if p == aprotPkgPath {
			return nil, fmt.Errorf("aprot: GoGeneratorOptions.ImportTypes cannot list %q: generated Go clients never import the server package", p)
		}
		g.importTypes[p] = true
	}
	g.named = make(map[reflect.Type]*goNamedType)
	g.order = nil
	g.importNames = make(map[string]string)
	g.realPkgNames = make(map[string]string)
	g.topLevel = nil
	g.importChecked = make(map[reflect.Type]bool)
	g.errs = nil
	g.meta = g.extractMeta()

	// Pass 1: discover every named type and import.
	g.rendering = false
	g.usedImports = make(map[string]bool)
	g.renderBody()
	if len(g.errs) > 0 {
		return nil, errors.Join(g.errs...)
	}
	if err := g.assignNames(); err != nil {
		return nil, err
	}
	g.assignImportNames()

	// Pass 2: render with final names.
	g.rendering = true
	g.usedImports = make(map[string]bool)
	body := g.renderBody()
	if len(g.errs) > 0 {
		return nil, errors.Join(g.errs...)
	}

	var src bytes.Buffer
	src.WriteString(generatedFileMarker + "\n\n")
	fmt.Fprintf(&src, "// Package %s is a typed Go client for an aprot API.\n", pkgName)
	fmt.Fprintf(&src, "package %s\n\n", pkgName)
	src.WriteString(g.renderImports())
	src.WriteString(body)

	formatted, err := format.Source(src.Bytes())
	if err != nil {
		return nil, fmt.Errorf("aprot: formatting generated Go client: %w\n%s", err, src.String())
	}

	results := map[string]string{goClientFileName: string(formatted)}

	if g.options.OutputDir != "" {
		// Generated client source is meant to be committed and read by the
		// toolchain, so conventional world-readable source perms are fine.
		if err := os.MkdirAll(g.options.OutputDir, 0o755); err != nil { // #nosec G301 -- generated source dir, not sensitive
			return nil, err
		}
		for name, content := range results {
			if err := os.WriteFile(filepath.Join(g.options.OutputDir, name), []byte(content), 0o644); err != nil { // #nosec G306 -- generated source file, not sensitive
				return nil, err
			}
		}
		if err := removeStaleGenerated(g.options.OutputDir, ".go", results); err != nil {
			return nil, err
		}
	}
	return results, nil
}

// packageName resolves the package clause of the generated code.
func (g *GoGenerator) packageName() (string, error) {
	name := g.options.PackageName
	if name == "" {
		if g.options.OutputDir != "" {
			if abs, err := filepath.Abs(g.options.OutputDir); err == nil {
				name = filepath.Base(abs)
			}
		}
		if !token.IsIdentifier(name) {
			name = "api"
		}
	}
	if !token.IsIdentifier(name) || name == "_" {
		return "", fmt.Errorf("aprot: Go package name %q is not a valid identifier", name)
	}
	return name, nil
}

// removeStaleGenerated deletes top-level files with the given suffix in dir
// that carry the aprot generated-code marker but were not written by the
// current run.
func removeStaleGenerated(dir, suffix string, written map[string]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, suffix) {
			continue
		}
		if _, ok := written[name]; ok {
			continue
		}
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path) // #nosec G304 -- path is confined to dir
		if err != nil || !isGeneratedByAprot(content) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// --- Groups, methods and push events ---------------------------------------

// goGroup is a handler group as the generated client sees it.
type goGroup struct {
	group     *HandlerGroup
	fieldName string // field on Client, e.g. "Todos"
	typeName  string // e.g. "TodosClient"
}

// clientGroups returns the socket-reachable groups sorted by name, the same
// set the TypeScript client emits.
func (g *GoGenerator) clientGroups() []goGroup {
	var out []goGroup
	for _, grp := range g.registry.Groups() {
		if !grp.socket {
			continue
		}
		field := goExportedIdent(grp.Name)
		out = append(out, goGroup{group: grp, fieldName: field, typeName: field + "Client"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].group.Name < out[j].group.Name })
	return out
}

// pushEvents returns the push events of socket-reachable groups, sorted by
// event name.
func (g *GoGenerator) pushEvents() []PushEventInfo {
	var out []PushEventInfo
	for _, grp := range g.clientGroups() {
		out = append(out, grp.group.PushEvents...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// renderBody renders everything below the import block. In the collection
// pass the output is discarded; only the side effects on g.named and the
// import set matter.
func (g *GoGenerator) renderBody() string {
	var b strings.Builder
	groups := g.clientGroups()

	// Client members must be unique: group fields, push methods, and the
	// members promoted from *client.Client.
	members := make(map[string]string)
	for _, m := range goPromotedClientMembers {
		members[m] = "the embedded *client.Client"
	}
	claim := func(name, what string) {
		if prev, ok := members[name]; ok {
			g.errorf("aprot: Go client: %s and %s both use the name Client.%s; rename one of them", what, prev, name)
			return
		}
		members[name] = what
	}
	for _, grp := range groups {
		claim(grp.fieldName, fmt.Sprintf("handler group %q", grp.group.Name))
	}
	events := g.pushEvents()
	for _, ev := range events {
		claim("On"+goExportedIdent(ev.Name), fmt.Sprintf("push event %q", ev.Name))
	}

	// Client and New.
	b.WriteString("// Client is the typed client for the API. It embeds the runtime\n")
	b.WriteString("// client, so Close, State, Done, Err and RefreshAuth are available\n")
	b.WriteString("// on it directly.\n")
	b.WriteString("type Client struct {\n")
	fmt.Fprintf(&b, "*%s.Client\n", g.qualPkg(goClientPkgPath))
	for _, grp := range groups {
		fmt.Fprintf(&b, "%s %s\n", grp.fieldName, grp.typeName)
	}
	b.WriteString("}\n\n")

	b.WriteString("// New wraps a connected runtime client in the typed client.\n")
	fmt.Fprintf(&b, "func New(c *%s.Client) *Client {\n", g.qualPkg(goClientPkgPath))
	b.WriteString("return &Client{\nClient: c,\n")
	for _, grp := range groups {
		fmt.Fprintf(&b, "%s: %s{c: c},\n", grp.fieldName, grp.typeName)
	}
	b.WriteString("}\n}\n\n")

	for _, ev := range events {
		dataType := g.typeExpr(ev.DataType)
		method := "On" + goExportedIdent(ev.Name)
		fmt.Fprintf(&b, "// %s calls fn for every %s push event.\n// The returned function removes the handler.\n", method, ev.Name)
		fmt.Fprintf(&b, "func (c *Client) %s(fn func(%s)) (remove func()) {\n", method, dataType)
		fmt.Fprintf(&b, "return %s.OnPush(c.Client, %s, fn)\n}\n\n", g.qualPkg(goClientPkgPath), strconv.Quote(ev.Name))
	}

	for _, grp := range groups {
		g.renderGroup(&b, grp)
	}

	g.renderErrorCodes(&b)

	// Every registered enum is declared, referenced or not, matching the
	// TypeScript client.
	for _, grp := range groups {
		for i := range grp.group.Enums {
			g.typeExpr(grp.group.Enums[i].Type)
		}
	}
	for i := range g.registry.SharedEnums() {
		g.typeExpr(g.registry.SharedEnums()[i].Type)
	}

	// Named type declarations. Rendering one can discover more, so the
	// collection pass iterates until the queue is drained.
	if g.rendering {
		sorted := append([]reflect.Type(nil), g.order...)
		sort.Slice(sorted, func(i, j int) bool { return g.named[sorted[i]].name < g.named[sorted[j]].name })
		for _, t := range sorted {
			g.renderNamed(&b, g.named[t])
		}
	} else {
		for i := 0; i < len(g.order); i++ {
			g.renderNamed(&b, g.named[g.order[i]])
		}
	}
	return b.String()
}

func (g *GoGenerator) renderGroup(b *strings.Builder, grp goGroup) {
	fmt.Fprintf(b, "// %s calls the methods of the %s handler group.\n", grp.typeName, grp.group.Name)
	fmt.Fprintf(b, "type %s struct {\nc *%s.Client\n}\n\n", grp.typeName, g.qualPkg(goClientPkgPath))

	names := make([]string, 0, len(grp.group.Handlers))
	for name := range grp.group.Handlers {
		names = append(names, name)
	}
	sort.Strings(names)

	methods := make(map[string]string)
	claim := func(name, handler string) {
		if prev, ok := methods[name]; ok {
			g.errorf("aprot: Go client: handlers %s.%s and %s.%s both generate method %s.%s",
				grp.group.Name, prev, grp.group.Name, handler, grp.typeName, name)
			return
		}
		methods[name] = handler
	}
	for _, name := range names {
		info := grp.group.Handlers[name]
		claim(name, name)
		if info.Kind == HandlerKindUnary && !info.IsVoid {
			claim("Subscribe"+name, name)
		}
	}

	pkgMeta := g.meta[g.groupPkgPath(grp.group)]
	for _, name := range names {
		g.renderMethod(b, grp, grp.group.Handlers[name], pkgMeta)
	}
}

// groupPkgPath returns the package path of the handler group's struct.
func (g *GoGenerator) groupPkgPath(grp *HandlerGroup) string {
	for _, info := range grp.Handlers {
		if info.handler.IsValid() {
			t := info.handler.Type()
			if t.Kind() == reflect.Pointer {
				t = t.Elem()
			}
			return t.PkgPath()
		}
	}
	return ""
}

// goParam is one rendered handler parameter.
type goParam struct {
	name     string
	typ      string
	variadic bool
}

func (g *GoGenerator) renderMethod(b *strings.Builder, grp goGroup, info *HandlerInfo, meta *goSourceMeta) {
	cl := g.qualPkg(goClientPkgPath)
	wire := strconv.Quote(grp.group.Name + "." + info.Name)

	var srcNames []string
	var doc string
	if meta != nil {
		srcNames = meta.paramNames(info.StructName, info.Name)
		doc = meta.handlerDoc(info.StructName, info.Name)
	}
	params := g.buildParams(info, srcNames)

	// sig declares the parameters; subSig is the same for the Subscribe
	// variant, whose trailing opts take the variadic slot, so a variadic
	// handler parameter is a plain slice there.
	ctxParam := "ctx " + g.qualPkg("context") + ".Context"
	decl, subDecl := []string{ctxParam}, []string{ctxParam}
	for _, p := range params {
		if p.variadic {
			decl = append(decl, p.name+" ..."+p.typ)
			subDecl = append(subDecl, p.name+" []"+p.typ)
		} else {
			decl = append(decl, p.name+" "+p.typ)
			subDecl = append(subDecl, p.name+" "+p.typ)
		}
	}
	sig, subSig := strings.Join(decl, ", "), strings.Join(subDecl, ", ")

	// paramsExpr is the []any passed to the runtime; prelude builds it when
	// a variadic parameter has to be spread into separate positional params.
	var prelude, paramsExpr string
	switch {
	case len(params) == 0:
		paramsExpr = "nil"
	case params[len(params)-1].variadic:
		var fixed []string
		for _, p := range params[:len(params)-1] {
			fixed = append(fixed, p.name)
		}
		last := params[len(params)-1].name
		prelude = fmt.Sprintf("params := []any{%s}\nfor _, x := range %s {\nparams = append(params, x)\n}\n", strings.Join(fixed, ", "), last)
		paramsExpr = "params"
	default:
		var all []string
		for _, p := range params {
			all = append(all, p.name)
		}
		paramsExpr = "[]any{" + strings.Join(all, ", ") + "}"
	}

	if strings.TrimSpace(doc) == "" {
		doc = fmt.Sprintf("%s calls the %s.%s handler.", info.Name, grp.group.Name, info.Name)
	}
	writeDoc(b, doc)
	recv := fmt.Sprintf("func (h %s) ", grp.typeName)
	out := info.method.Type().Out(0)

	switch info.Kind {
	case HandlerKindStream:
		_, elem, _ := streamTypes(out)
		item := g.typeExpr(elem)
		fmt.Fprintf(b, "%s%s(%s) *%s.StreamResult[%s] {\n%sreturn %s.Stream[%s](ctx, h.c, %s, %s)\n}\n\n",
			recv, info.Name, sig, cl, item, prelude, cl, item, wire, paramsExpr)
		return
	case HandlerKindStream2:
		key, val, _ := streamTypes(out)
		k, v := g.typeExpr(key), g.typeExpr(val)
		fmt.Fprintf(b, "%s%s(%s) *%s.Stream2Result[%s, %s] {\n%sreturn %s.Stream2[%s, %s](ctx, h.c, %s, %s)\n}\n\n",
			recv, info.Name, sig, cl, k, v, prelude, cl, k, v, wire, paramsExpr)
		return
	}

	if info.IsVoid {
		fmt.Fprintf(b, "%s%s(%s) error {\n%s_, err := %s.Call[struct{}](ctx, h.c, %s, %s)\nreturn err\n}\n\n",
			recv, info.Name, sig, prelude, cl, wire, paramsExpr)
		return
	}

	result := g.typeExpr(out)
	fmt.Fprintf(b, "%s%s(%s) (%s, error) {\n%sreturn %s.Call[%s](ctx, h.c, %s, %s)\n}\n\n",
		recv, info.Name, sig, result, prelude, cl, result, wire, paramsExpr)

	sub := "Subscribe" + info.Name
	fmt.Fprintf(b, "// %s runs %s as a live query.\n// Each result arrives on the subscription's C; close it with Close.\n", sub, info.Name)
	fmt.Fprintf(b, "%s%s(%s, opts ...%s.SubscribeOption[%s]) *%s.Subscription[%s] {\n%sreturn %s.Subscribe[%s](ctx, h.c, %s, %s, opts...)\n}\n\n",
		recv, sub, subSig, cl, result, cl, result, prelude, cl, result, wire, paramsExpr)
}

// streamTypes returns the key (nil for iter.Seq) and value types of an
// iter.Seq or iter.Seq2 return type, without unwrapping pointers.
func streamTypes(t reflect.Type) (key, val reflect.Type, ok bool) {
	if elem, ok := isIterSeq(t); ok {
		return nil, elem, true
	}
	if k, v, ok := isIterSeq2(t); ok {
		return k, v, true
	}
	return nil, nil, false
}

// goReservedParamNames are identifiers the generated method bodies use, plus
// Go's predeclared identifiers, which a parameter must not shadow.
var goReservedParamNames = map[string]bool{
	"ctx": true, "h": true, "c": true, "opts": true, "params": true, "x": true, "err": true,
	// predeclared
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true, "int8": true, "int16": true,
	"int32": true, "int64": true, "rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true, "true": true, "false": true,
	"iota": true, "nil": true, "append": true, "cap": true, "clear": true, "close": true,
	"complex": true, "copy": true, "delete": true, "imag": true, "len": true, "make": true,
	"max": true, "min": true, "new": true, "panic": true, "print": true, "println": true,
	"real": true, "recover": true,
}

// buildParams names and types a handler's parameters. Names come from the
// handler source where available, else arg0, arg1, ...
func (g *GoGenerator) buildParams(info *HandlerInfo, srcNames []string) []goParam {
	used := make(map[string]bool)
	params := make([]goParam, len(info.Params))
	for i, p := range info.Params {
		name := ""
		if i < len(srcNames) {
			name = srcNames[i]
		}
		if name == "" || name == "_" || name == "arg" || !token.IsIdentifier(name) {
			name = fmt.Sprintf("arg%d", i)
		}
		for goReservedParamNames[name] || g.isImportName(name) || g.topLevel[name] || used[name] {
			name += "_"
		}
		used[name] = true
		params[i] = goParam{name: name, typ: g.typeExpr(p.Type), variadic: p.Variadic}
	}
	return params
}

func (g *GoGenerator) isImportName(name string) bool {
	for _, local := range g.importNames {
		if local == name {
			return true
		}
	}
	return false
}

// renderErrorCodes emits the registry's custom error codes as constants.
func (g *GoGenerator) renderErrorCodes(b *strings.Builder) {
	codes := g.registry.ErrorCodes()
	if len(codes) == 0 {
		return
	}
	b.WriteString("// Custom error codes registered on the server. Test an error for one\n")
	fmt.Fprintf(b, "// with %s.HasCode(err, ErrCode...).\n", g.qualPkg(goClientPkgPath))
	b.WriteString("const (\n")
	for _, ec := range codes {
		fmt.Fprintf(b, "%s = %d\n", "ErrCode"+goExportedIdent(ec.Name), ec.Code)
	}
	b.WriteString(")\n\n")
}

// --- Named types -------------------------------------------------------------

// namedRef registers t as a type the generated package declares and returns
// its name. marshaled is set for an enum whose own marshaler encodes its
// members as strings.
func (g *GoGenerator) namedRef(t reflect.Type, enum *EnumInfo, marshaled []string) string {
	if nt, ok := g.named[t]; ok {
		if nt.name != "" {
			return nt.name
		}
		return goTypeNameCandidate(t, 0)
	}
	if g.rendering {
		// Every type is discovered in the collection pass; reaching here
		// would mean the passes diverged.
		g.errorf("aprot: Go client: internal error: type %s discovered while rendering", t)
		return "any"
	}
	nt := &goNamedType{t: t, enum: enum, marshaled: marshaled}
	g.named[t] = nt
	g.order = append(g.order, t)
	return goTypeNameCandidate(t, 0)
}

// Patterns that turn the type arguments of a reflected generic type name,
// such as "Page[*github.com/x/pkg.Item]", into words.
var (
	genericArrayRe = regexp.MustCompile(`\[(\d+)\]`)
	genericMapRe   = regexp.MustCompile(`\bmap\[`)
	genericQualRe  = regexp.MustCompile(`(?:[^\s\[\](),*]*/)?([\p{L}_][\p{L}\p{N}_]*)\.([\p{L}_][\p{L}\p{N}_]*)`)
)

// goTypeNameCandidate is a generated name for the named type t. Higher levels
// are tried when lower ones collide:
//
//   - level 0: the type's own name, exported. Generic type arguments are
//     folded in as words, with pointers, slices, arrays and maps spelled
//     out: "Page[*pkg.Item]" → "PagePtrItem".
//   - level 1: as level 0, but each type argument keeps its package name:
//     "Page[other.Item]" → "PageOtherItem".
//   - level 2: as level 1, prefixed with the type's own package name:
//     "Item" from package other → "OtherItem".
func goTypeNameCandidate(t reflect.Type, level int) string {
	name := t.Name()
	if i := strings.IndexByte(name, '['); i >= 0 && strings.HasSuffix(name, "]") {
		args := name[i+1 : len(name)-1]
		args = genericArrayRe.ReplaceAllString(args, " Array$1 ")
		args = strings.ReplaceAll(args, "[]", " Slice ")
		args = genericMapRe.ReplaceAllString(args, " Map ")
		args = strings.ReplaceAll(args, "*", " Ptr ")
		args = strings.ReplaceAll(args, "chan ", " Chan ")
		args = strings.ReplaceAll(args, "func(", " Func ")
		if level >= 1 {
			args = genericQualRe.ReplaceAllString(args, " $1 $2 ")
		} else {
			args = genericQualRe.ReplaceAllString(args, " $2 ")
		}
		name = name[:i] + " " + args
	}
	name = goExportedIdent(name)
	if level >= 2 {
		name = goExportedIdent(goPkgName(t.PkgPath())) + name
	}
	return name
}

// assignNames gives every named type its final name. Types whose names
// collide, with each other or with a fixed identifier of the generated
// package, move up through the levels of goTypeNameCandidate: generic
// arguments keep their package, then the type gets its package as a prefix
// ("Item" from package other becomes "OtherItem"), as the TypeScript
// generator separates such types into per-package files. Whatever still
// collides gets a numeric suffix in a fixed order (RvDup, RvDup2).
func (g *GoGenerator) assignNames() error {
	fixed := map[string]string{"Client": "the generated Client type", "New": "the generated New function"}
	for _, grp := range g.clientGroups() {
		fixed[grp.typeName] = fmt.Sprintf("the client type of handler group %q", grp.group.Name)
	}

	for _, t := range g.order {
		g.named[t].level = 0
	}
	for range 4 {
		groups := make(map[string][]reflect.Type)
		for _, t := range g.order {
			name := goTypeNameCandidate(t, g.named[t].level)
			groups[name] = append(groups[name], t)
		}
		changed := false
		for name, types := range groups {
			_, isFixed := fixed[name]
			if len(types) < 2 && !isFixed {
				continue
			}
			for _, t := range types {
				if g.named[t].level < 2 {
					g.named[t].level++
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	sorted := append([]reflect.Type(nil), g.order...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		na, nb := goTypeNameCandidate(a, g.named[a].level), goTypeNameCandidate(b, g.named[b].level)
		if na != nb {
			return na < nb
		}
		if a.PkgPath() != b.PkgPath() {
			return a.PkgPath() < b.PkgPath()
		}
		return a.String() < b.String()
	})
	taken := make(map[string]reflect.Type)
	for _, t := range sorted {
		base := goTypeNameCandidate(t, g.named[t].level)
		name := base
		for n := 2; ; n++ {
			_, isFixed := fixed[name]
			if _, isTaken := taken[name]; !isFixed && !isTaken {
				break
			}
			name = fmt.Sprintf("%s%d", base, n)
		}
		taken[name] = t
		g.named[t].name = name
	}

	// Constants share the namespace: enum members and error codes.
	var errs []error
	consts := make(map[string]string)
	claim := func(name, what string) {
		if other, ok := taken[name]; ok {
			errs = append(errs, fmt.Errorf("aprot: Go client: %s generates the name %s, which collides with type %s", what, name, other))
			return
		}
		if prev, ok := fixed[name]; ok {
			errs = append(errs, fmt.Errorf("aprot: Go client: %s generates the name %s, which collides with %s", what, name, prev))
			return
		}
		if prev, ok := consts[name]; ok {
			errs = append(errs, fmt.Errorf("aprot: Go client: %s and %s both generate the name %s", prev, what, name))
			return
		}
		consts[name] = what
	}
	for _, t := range g.order {
		nt := g.named[t]
		if nt.enum == nil {
			continue
		}
		for i, v := range nt.enum.Values {
			claim(nt.name+goEnumMemberIdent(v.Name, i), fmt.Sprintf("enum value %s.%s", nt.enum.Name, v.Name))
		}
	}
	for _, ec := range g.registry.ErrorCodes() {
		claim("ErrCode"+goExportedIdent(ec.Name), fmt.Sprintf("error code %q", ec.Name))
	}

	g.topLevel = make(map[string]bool, len(fixed)+len(taken)+len(consts))
	for name := range fixed {
		g.topLevel[name] = true
	}
	for name := range taken {
		g.topLevel[name] = true
	}
	for name := range consts {
		g.topLevel[name] = true
	}
	return errors.Join(errs...)
}

// renderNamed writes the declaration of one named type.
func (g *GoGenerator) renderNamed(b *strings.Builder, nt *goNamedType) {
	name := nt.name
	if name == "" {
		name = goTypeNameCandidate(nt.t, 0)
	}
	doc := ""
	if m := g.meta[nt.t.PkgPath()]; m != nil {
		doc = m.typeDocs[nt.t.Name()]
		// Doc comments start with the declared name; follow a rename.
		if rest, ok := strings.CutPrefix(doc, nt.t.Name()+" "); ok && name != nt.t.Name() {
			doc = name + " " + rest
		}
	}
	if doc == "" {
		doc = fmt.Sprintf("%s is the wire shape of %s.", name, nt.t.String())
	}
	writeDoc(b, doc)

	if nt.enum != nil {
		kind := goBasicKind(nt.t.Kind())
		if nt.marshaled != nil {
			kind = "string"
		}
		fmt.Fprintf(b, "type %s %s\n\n", name, kind)
		b.WriteString("const (\n")
		for i, v := range nt.enum.Values {
			var lit string
			switch {
			case nt.marshaled != nil:
				lit = strconv.Quote(nt.marshaled[i])
			case nt.enum.IsString:
				lit = strconv.Quote(fmt.Sprint(v.Value))
			default:
				lit = fmt.Sprint(v.Value)
			}
			fmt.Fprintf(b, "%s%s %s = %s\n", name, goEnumMemberIdent(v.Name, i), name, lit)
		}
		b.WriteString(")\n\n")
		return
	}
	fmt.Fprintf(b, "type %s %s\n\n", name, g.underlyingExpr(nt.t))
}

// --- Type mapping ------------------------------------------------------------

// typeExpr returns the Go type expression the generated code uses for the
// server type t. See GoGenerator for the rules.
func (g *GoGenerator) typeExpr(t reflect.Type) string {
	if t == nil {
		return "any"
	}
	if t == blobType {
		return g.qual(goClientPkgPath, "Blob")
	}
	if t.Kind() == reflect.Pointer {
		// A pointer to a flattened sql.Null* is value-or-null on the wire,
		// like the flattened type, which is a pointer already.
		if isSQLNullFlattened(t.Elem()) {
			expr, _ := g.sqlNullExpr(t.Elem())
			return expr
		}
		return "*" + g.typeExpr(t.Elem())
	}
	if expr, ok := g.sqlNullExpr(t); ok {
		return expr
	}
	if t.Kind() == reflect.Interface {
		return "any"
	}
	pkg, name := t.PkgPath(), t.Name()
	if name == "" || pkg == "" {
		return g.underlyingExpr(t)
	}
	generic := strings.Contains(name, "[")
	if isStdlibPkg(pkg) {
		if !generic && token.IsExported(name) {
			return g.qual(pkg, name)
		}
		if shape := g.marshalShape(t); shape != "" {
			return g.shapeTypeExpr(shape)
		}
		return g.underlyingExpr(t)
	}
	if g.importTypes[pkg] {
		if generic {
			g.errorf("aprot: Go client: cannot import generic type %s from %s; remove the package from ImportTypes", t, pkg)
			return "any"
		}
		if !token.IsExported(name) {
			g.errorf("aprot: Go client: cannot import unexported type %s from %s; remove the package from ImportTypes", t, pkg)
			return "any"
		}
		g.checkImportedType(t)
		if real, _, ok := strings.Cut(t.String(), "."); ok && token.IsIdentifier(real) {
			g.realPkgNames[pkg] = real
		}
		return g.qual(pkg, name)
	}
	enum := g.registry.GetEnum(t)
	// The type's own marshaler decides the wire shape, even for a
	// registered enum: an int enum with MarshalText travels as "High".
	if shape := g.marshalShape(t); shape != "" {
		if enum != nil && shape == goShapeString {
			if values, ok := g.marshaledEnumValues(t, enum); ok {
				return g.namedRef(t, enum, values)
			}
		}
		return g.shapeTypeExpr(shape)
	}
	if enum != nil {
		return g.namedRef(t, enum, nil)
	}
	switch t.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		// Not encodable; the server fails on it at runtime.
		return "any"
	}
	return g.namedRef(t, nil, nil)
}

// marshaledEnumValues marshals every member of an enum with its own
// marshaler and returns the JSON string values, or ok=false when a member
// does not encode as a string.
func (g *GoGenerator) marshaledEnumValues(t reflect.Type, enum *EnumInfo) ([]string, bool) {
	values := make([]string, len(enum.Values))
	for i, v := range enum.Values {
		pv := reflect.New(t)
		switch val := v.Value.(type) {
		case string:
			pv.Elem().SetString(val)
		case int64:
			pv.Elem().SetInt(val)
		default:
			return nil, false
		}
		data, err := marshalJSON(pv.Interface())
		if err != nil {
			g.errorf("aprot: Go client: marshaling enum %s value %v: %w", enum.Name, v.Value, err)
			return nil, false
		}
		var s string
		if err := jsonv2.Unmarshal(data, &s); err != nil {
			return nil, false
		}
		values[i] = s
	}
	return values, true
}

// underlyingExpr renders t's structure, ignoring its name.
func (g *GoGenerator) underlyingExpr(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + g.typeExpr(t.Elem())
	case reflect.Slice:
		return "[]" + g.typeExpr(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), g.typeExpr(t.Elem()))
	case reflect.Map:
		return "map[" + g.mapKeyExpr(t.Key()) + "]" + g.typeExpr(t.Elem())
	case reflect.Struct:
		return g.structExpr(t)
	case reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		return "any"
	default:
		return goBasicKind(t.Kind())
	}
}

// mapKeyExpr is the generated type of a map key. A key whose own marshaler
// decides its encoding is a JSON object key, so a string, whatever shape the
// marshaler has for values.
func (g *GoGenerator) mapKeyExpr(k reflect.Type) string {
	if k.Name() != "" && k.PkgPath() != "" && !isStdlibPkg(k.PkgPath()) && !g.importTypes[k.PkgPath()] {
		if shape := g.marshalShape(k); shape != "" && shape != goShapeString {
			return "string"
		}
	}
	return g.typeExpr(k)
}

// goBasicKind names the predeclared type of a basic kind.
func goBasicKind(k reflect.Kind) string {
	if k == reflect.Uint8 {
		return "byte"
	}
	return k.String()
}

// embeddableRe matches a type expression that can be embedded: a possibly
// qualified identifier.
var embeddableRe = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]*(\.[\p{L}_][\p{L}\p{N}_]*)?$`)

// goField is one field of a generated struct.
type goField struct {
	goName  string // Go field name; for an embedded field, its type name
	jsonKey string // key on the wire; empty for an embedded (inlined) field
	decl    string // the declaration without the tag
	tag     string // rendered tag, or ""
	doc     string

	// For an embedded field: the server's embedded struct type and whether
	// it was embedded by pointer.
	embedded reflect.Type
	ptr      bool
}

// structExpr renders a struct type with the wire shape of t: same field
// names and json tags. Embedded structs without a JSON name, which the
// encoder inlines, stay embedded so the same inlining rules apply on the
// client.
func (g *GoGenerator) structExpr(t reflect.Type) string {
	fields := g.structFields(t)

	// An embedded type is named after its generated type, which can clash
	// with a sibling field the server is allowed to have (an unexported
	// embedded inner next to a field Inner). Inline such an embed's fields,
	// minus any a shallower field on the server shadows.
	direct := make(map[string]bool)
	keys := make(map[string]bool)
	for _, f := range fields {
		if f.embedded == nil {
			direct[f.goName] = true
			keys[f.jsonKey] = true
		}
	}
	var out []goField
	for _, f := range fields {
		if f.embedded == nil || !direct[f.goName] {
			out = append(out, f)
			continue
		}
		if f.ptr {
			g.errorf("aprot: Go client: %s embeds *%s, whose generated name %s clashes with a field of the same name; rename one of them",
				t, f.embedded, f.goName)
			continue
		}
		for _, inner := range g.structFields(f.embedded) {
			if inner.embedded == nil && keys[inner.jsonKey] {
				continue
			}
			out = append(out, inner)
		}
	}

	var b strings.Builder
	b.WriteString("struct {\n")
	seen := make(map[string]bool)
	for _, f := range out {
		if seen[f.goName] {
			g.errorf("aprot: Go client: %s has two fields named %s after mapping; rename one of them", t, f.goName)
			continue
		}
		seen[f.goName] = true
		writeDoc(&b, f.doc)
		line := f.decl
		if f.tag != "" {
			line += " " + f.tag
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("}")
	return b.String()
}

// structFields maps the fields of struct type t.
func (g *GoGenerator) structFields(t reflect.Type) []goField {
	var meta *goSourceMeta
	if t.Name() != "" {
		meta = g.meta[t.PkgPath()]
	}
	var fields []goField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if shouldSkipField(f) {
			continue
		}
		tag, hasTag := f.Tag.Lookup("json")
		jsonName, _, _ := strings.Cut(tag, ",")

		var field goField
		embeddedStruct := false
		if f.Anonymous {
			ft, ptr := f.Type, false
			if ft.Kind() == reflect.Pointer {
				ft, ptr = ft.Elem(), true
			}
			if ft.Kind() == reflect.Struct {
				// json/v2 encodes an embedded struct even when its type is
				// unexported: inlined without a JSON name, as a named
				// field with one.
				embeddedStruct = true
				if jsonName == "" {
					if isStdlibPkg(ft.PkgPath()) && !hasExportedField(ft) {
						// e.g. an embedded sync.Mutex: nothing on the wire.
						continue
					}
					var expr string
					if ft == blobType || ft.PkgPath() == "database/sql" {
						// The server inlines the struct's own fields, not
						// the shape these types have elsewhere (client.Blob,
						// whose UnmarshalJSON would be promoted, or a
						// flattened pointer). Declare a struct with them.
						expr = g.namedRef(ft, nil, nil)
					} else {
						expr = g.typeExpr(ft)
					}
					if embeddableRe.MatchString(expr) {
						decl := expr
						if ptr {
							decl = "*" + expr
						}
						field = goField{
							goName:   expr[strings.LastIndex(expr, ".")+1:],
							decl:     decl,
							embedded: ft,
							ptr:      ptr,
						}
					}
				}
			}
		}
		if field.decl == "" {
			if !f.IsExported() && !embeddedStruct {
				continue
			}
			name := goExportedIdent(f.Name)
			key := jsonName
			if key == "" {
				key = f.Name
			}
			field = goField{goName: name, jsonKey: key, decl: name + " " + g.fieldTypeExpr(t, f)}
			if hasTag && g.serverIgnoresTagOptions(g.effectiveType(t, f)) {
				tag = stripTypeTagOptions(tag)
			}
		}
		if hasTag {
			field.tag = goStructTag("json", tag)
		}
		if meta != nil {
			field.doc = meta.fieldDoc(t.Name(), f.Name)
		}
		fields = append(fields, field)
	}
	return fields
}

func hasExportedField(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).IsExported() {
			return true
		}
	}
	return false
}

// effectiveType is the field's codegen override, or its declared type.
func (g *GoGenerator) effectiveType(owner reflect.Type, f reflect.StructField) reflect.Type {
	if ov := g.registry.fieldTypeOverride(owner, f.Name); ov != nil {
		return ov
	}
	return f.Type
}

// fieldTypeExpr is the generated type of a struct field. A field with a
// [Registry.OverrideFieldType] override is declared as an interface on the
// server, so it can be nil and travel as null; the override type becomes a
// pointer unless it can already hold nil.
func (g *GoGenerator) fieldTypeExpr(owner reflect.Type, f reflect.StructField) string {
	ov := g.registry.fieldTypeOverride(owner, f.Name)
	if ov == nil {
		return g.typeExpr(f.Type)
	}
	switch ov.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return g.typeExpr(ov)
	}
	return g.typeExpr(reflect.PointerTo(ov))
}

// serverIgnoresTagOptions reports whether the server encodes a field of type
// t without regard to its `string` and `format:` tag options: a flattened
// sql.Null*, whose marshaler is registered for the type, or a type with its
// own marshaler. The generated client type is a plain value that would
// honor them, so they are not copied.
func (g *GoGenerator) serverIgnoresTagOptions(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isSQLNullFlattened(t) {
		return true
	}
	if t == blobType || t.Name() == "" || t.PkgPath() == "" || isStdlibPkg(t.PkgPath()) || g.importTypes[t.PkgPath()] {
		return false
	}
	return g.marshalShape(t) != ""
}

// stripTypeTagOptions removes the `string` and `format:` options from a json
// tag value.
func stripTypeTagOptions(tag string) string {
	parts := strings.Split(tag, ",")
	kept := parts[:1]
	for _, p := range parts[1:] {
		if p == "string" || strings.HasPrefix(p, "format:") {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ",")
}

// goStructTag renders a single-key struct tag literal.
func goStructTag(key, value string) string {
	tag := key + ":" + strconv.Quote(value)
	if strings.Contains(tag, "`") {
		return strconv.Quote(tag)
	}
	return "`" + tag + "`"
}

// sqlNullFlattened lists the sql.Null[T] instantiations the server flattens
// to value-or-null (see sqlNullMarshalers). Other instantiations keep the
// default {"V": ..., "Valid": ...} shape.
var sqlNullFlattened = map[reflect.Type]bool{
	reflect.TypeFor[string]():    true,
	reflect.TypeFor[int]():       true,
	reflect.TypeFor[int64]():     true,
	reflect.TypeFor[int32]():     true,
	reflect.TypeFor[int16]():     true,
	reflect.TypeFor[float64]():   true,
	reflect.TypeFor[bool]():      true,
	reflect.TypeFor[time.Time](): true,
}

// isSQLNullFlattened reports whether the server encodes t, a database/sql
// nullable type, as its value or null.
func isSQLNullFlattened(t reflect.Type) bool {
	if t.PkgPath() != "database/sql" {
		return false
	}
	switch t.Name() {
	case "NullString", "NullInt64", "NullInt32", "NullInt16", "NullFloat64", "NullBool", "NullByte", "NullTime":
		return true
	}
	if strings.HasPrefix(t.Name(), "Null[") {
		if v, ok := t.FieldByName("V"); ok {
			return sqlNullFlattened[v.Type]
		}
	}
	return false
}

// sqlNullExpr maps the database/sql nullable types: the ones the server
// flattens become a pointer to their value type, a sql.Null[T] it does not
// flatten keeps its {V, Valid} struct shape.
func (g *GoGenerator) sqlNullExpr(t reflect.Type) (string, bool) {
	if t.PkgPath() != "database/sql" {
		return "", false
	}
	switch t.Name() {
	case "NullString":
		return "*string", true
	case "NullInt64":
		return "*int64", true
	case "NullInt32":
		return "*int32", true
	case "NullInt16":
		return "*int16", true
	case "NullFloat64":
		return "*float64", true
	case "NullBool":
		return "*bool", true
	case "NullByte":
		return "*byte", true
	case "NullTime":
		return "*" + g.qual("time", "Time"), true
	}
	if strings.HasPrefix(t.Name(), "Null[") {
		if v, ok := t.FieldByName("V"); ok {
			if sqlNullFlattened[v.Type] {
				return "*" + g.typeExpr(v.Type), true
			}
			return g.underlyingExpr(t), true
		}
	}
	return "", false
}

// Wire shapes of a type with its own marshaler.
const (
	goShapeString = "string"
	goShapeBool   = "bool"
	goShapeRaw    = "raw"
)

// marshalShape reports the wire shape of a type with its own JSON or text
// marshaler, or "" when it has none. A text marshaler (MarshalText or
// AppendText) always produces a JSON string. For a JSON marshaler the zero
// value is marshaled: a string is a string, a bool a bool, and anything else
// raw JSON. One sample cannot tell the precision of a number or the element
// encoding of an array, so those are not guessed.
func (g *GoGenerator) marshalShape(t reflect.Type) string {
	pt := reflect.PointerTo(t)
	hasJSON := t.Implements(jsonMarshalerType) || pt.Implements(jsonMarshalerType) ||
		t.Implements(jsonMarshalerToType) || pt.Implements(jsonMarshalerToType)
	if !hasJSON {
		if t.Implements(textMarshalerIface) || pt.Implements(textMarshalerIface) ||
			t.Implements(textAppenderIface) || pt.Implements(textAppenderIface) {
			return goShapeString
		}
		return ""
	}
	var data []byte
	func() {
		defer func() { _ = recover() }()
		out, err := marshalJSON(reflect.New(t).Interface())
		if err == nil {
			data = out
		}
	}()
	if len(data) > 0 {
		switch data[0] {
		case '"':
			return goShapeString
		case 't', 'f':
			return goShapeBool
		}
	}
	return goShapeRaw
}

// shapeTypeExpr is the generated type of a marshaler shape.
func (g *GoGenerator) shapeTypeExpr(shape string) string {
	switch shape {
	case goShapeString:
		return "string"
	case goShapeBool:
		return "bool"
	}
	return g.qual("encoding/json/jsontext", "Value")
}

// checkImportedType records an error when an ImportTypes type holds a
// database/sql nullable value at any depth. The server flattens those to
// value-or-null; the client decodes the imported type as declared, without
// that flattening, so it could not read what the server sends.
func (g *GoGenerator) checkImportedType(t reflect.Type) {
	if g.importChecked[t] {
		return
	}
	g.importChecked[t] = true
	if path := findSQLNull(t, nil, map[reflect.Type]bool{}); path != nil {
		g.errorf("aprot: Go client: %s from ImportTypes package %s holds %s at %s. The server sends sql.Null* values flattened to value-or-null, and the client does not flatten them, so it could not decode this type; remove %s from ImportTypes so its wire shape is copied instead",
			t, t.PkgPath(), path[len(path)-1], strings.Join(path[:len(path)-1], "."), t.PkgPath())
	}
}

// findSQLNull returns the field path to a database/sql Null* type inside t,
// ending with that type's name, or nil.
func findSQLNull(t reflect.Type, path []string, seen map[reflect.Type]bool) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.PkgPath() == "database/sql" && strings.HasPrefix(t.Name(), "Null") {
		return append(append([]string(nil), path...), t.String())
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	pt := reflect.PointerTo(t)
	if t.Implements(jsonMarshalerType) || pt.Implements(jsonMarshalerType) ||
		t.Implements(jsonMarshalerToType) || pt.Implements(jsonMarshalerToType) {
		// Its own marshaler decides the encoding.
		return nil
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return findSQLNull(t.Elem(), append(path, "[]"), seen)
	case reflect.Map:
		return findSQLNull(t.Elem(), append(path, "[]"), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if shouldSkipField(f) || (!f.IsExported() && !f.Anonymous) {
				continue
			}
			if p := findSQLNull(f.Type, append(path, f.Name), seen); p != nil {
				return p
			}
		}
	}
	return nil
}

// --- Imports -------------------------------------------------------------

// qual returns pkg.name for a type in another package, recording the import.
func (g *GoGenerator) qual(pkgPath, name string) string {
	return g.qualPkg(pkgPath) + "." + name
}

// qualPkg returns the local name of an imported package and records the
// import as used.
func (g *GoGenerator) qualPkg(pkgPath string) string {
	g.usedImports[pkgPath] = true
	if local, ok := g.importNames[pkgPath]; ok {
		return local
	}
	local := g.pkgName(pkgPath)
	if !g.rendering {
		g.importNames[pkgPath] = local
	}
	return local
}

// assignImportNames gives every import a unique local name. Paths are
// processed in sorted order, so a clash is resolved the same way every run.
func (g *GoGenerator) assignImportNames() {
	paths := make([]string, 0, len(g.importNames))
	for p := range g.importNames {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	used := make(map[string]bool)
	for _, p := range paths {
		base := g.pkgName(p)
		local := base
		for n := 2; used[local] || token.IsKeyword(local) || goReservedParamNames[local]; n++ {
			local = fmt.Sprintf("%s%d", base, n)
		}
		used[local] = true
		g.importNames[p] = local
	}
}

func (g *GoGenerator) renderImports() string {
	var std, other []string
	for p := range g.usedImports {
		line := strconv.Quote(p)
		if local := g.importNames[p]; local != g.pkgName(p) || g.pkgNameIsGuess(p) {
			line = local + " " + line
		}
		if isStdlibPkg(p) {
			std = append(std, line)
		} else {
			other = append(other, line)
		}
	}
	if len(std)+len(other) == 0 {
		return ""
	}
	sort.Strings(std)
	sort.Strings(other)
	var b strings.Builder
	b.WriteString("import (\n")
	for _, l := range std {
		b.WriteString(l + "\n")
	}
	if len(std) > 0 && len(other) > 0 {
		b.WriteString("\n")
	}
	for _, l := range other {
		b.WriteString(l + "\n")
	}
	b.WriteString(")\n\n")
	return b.String()
}

var majorVersionRe = regexp.MustCompile(`^v[0-9]+$`)

// goPkgName guesses a package's name from its import path: the last element,
// skipping a major-version suffix, made into an identifier.
func goPkgName(pkgPath string) string {
	parts := strings.Split(pkgPath, "/")
	name := parts[len(parts)-1]
	if majorVersionRe.MatchString(name) && len(parts) > 1 {
		name = parts[len(parts)-2]
	}
	name = strings.TrimPrefix(name, "go-")
	var b strings.Builder
	for _, r := range name {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	out := b.String()
	if out == "" || !unicode.IsLetter([]rune(out)[0]) {
		out = "pkg" + out
	}
	return out
}

// pkgName is the package clause of pkgPath: read from reflect for imported
// types, guessed from the path otherwise.
func (g *GoGenerator) pkgName(pkgPath string) string {
	if real, ok := g.realPkgNames[pkgPath]; ok {
		return real
	}
	return goPkgName(pkgPath)
}

// pkgNameIsGuess reports whether the import of pkgPath should carry an
// explicit name because its package clause may differ from the last path
// element.
func (g *GoGenerator) pkgNameIsGuess(pkgPath string) bool {
	if pkgPath == goClientPkgPath || isStdlibPkg(pkgPath) {
		return false
	}
	return pkgPath[strings.LastIndex(pkgPath, "/")+1:] != g.pkgName(pkgPath)
}

var (
	stdlibMu    sync.Mutex
	stdlibCache = map[string]bool{}
)

// isStdlibPkg reports whether pkgPath is a standard library package. A path
// whose first element has no dot is standard if it exists under GOROOT; when
// GOROOT is unknown the dot rule alone decides.
func isStdlibPkg(pkgPath string) bool {
	if pkgPath == "" {
		return false
	}
	stdlibMu.Lock()
	defer stdlibMu.Unlock()
	if v, ok := stdlibCache[pkgPath]; ok {
		return v
	}
	first, _, _ := strings.Cut(pkgPath, "/")
	std := !strings.Contains(first, ".")
	if std {
		if root := build.Default.GOROOT; root != "" {
			if fi, err := os.Stat(filepath.Join(root, "src", filepath.FromSlash(pkgPath))); err != nil || !fi.IsDir() {
				std = false
			}
		}
	}
	stdlibCache[pkgPath] = std
	return std
}

// --- Names and docs ------------------------------------------------------

// goExportedIdent turns s into an exported Go identifier: characters that
// cannot appear in one start a new capitalized word ("in-progress" →
// "InProgress"), and the first letter is upper-cased.
func goExportedIdent(s string) string {
	var b strings.Builder
	upper := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			if upper {
				r = unicode.ToUpper(r)
				upper = false
			}
			b.WriteRune(r)
		default:
			upper = true
		}
	}
	out := b.String()
	if out == "" {
		return "X"
	}
	if first := []rune(out)[0]; !unicode.IsUpper(first) {
		out = "X" + out
	}
	return out
}

// goEnumMemberIdent is the suffix of an enum member constant: the member
// name exported, or ValueN when it has no usable characters.
func goEnumMemberIdent(name string, i int) string {
	if strings.IndexFunc(name, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
		return fmt.Sprintf("Value%d", i)
	}
	id := goExportedIdent(name)
	if strings.HasPrefix(id, "X") && !strings.HasPrefix(name, "X") && !strings.HasPrefix(name, "x") {
		// Started with a digit or underscore; the enum type name in front
		// keeps it a valid identifier, so drop the placeholder.
		id = id[1:]
	}
	return id
}

// writeDoc writes doc as a // comment block.
func writeDoc(b *strings.Builder, doc string) {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return
	}
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			b.WriteString("//\n")
		} else {
			b.WriteString("// " + line + "\n")
		}
	}
}

// extractMeta parses the source of every client group's package for docs
// and parameter names, keyed by package path.
func (g *GoGenerator) extractMeta() map[string]*goSourceMeta {
	dirs := make(map[string]string) // pkg path → dir
	for _, grp := range g.clientGroups() {
		if dir := grp.group.SourceDir(); dir != "" {
			dirs[g.groupPkgPath(grp.group)] = dir
		}
	}
	out := make(map[string]*goSourceMeta, len(dirs))
	for pkg, dir := range dirs {
		out[pkg] = &goSourceMeta{
			sourceMeta: extractSourceMeta(map[string]bool{dir: true}),
			typeDocs:   extractTypeDocs(dir),
		}
	}
	return out
}

// extractTypeDocs collects the doc comments of every type declaration in
// dir, including non-struct types such as enums.
func extractTypeDocs(dir string) map[string]string {
	docs := make(map[string]string)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return docs
	}
	pkgNames := make([]string, 0, len(pkgs))
	for name := range pkgs {
		pkgNames = append(pkgNames, name)
	}
	sort.Strings(pkgNames)
	for _, pn := range pkgNames {
		files := make([]string, 0, len(pkgs[pn].Files))
		for name := range pkgs[pn].Files {
			files = append(files, name)
		}
		sort.Strings(files)
		for _, fn := range files {
			for _, decl := range pkgs[pn].Files[fn].Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts := spec.(*ast.TypeSpec)
					doc := ts.Doc.Text()
					if doc == "" && len(gd.Specs) == 1 {
						doc = gd.Doc.Text()
					}
					if doc = strings.TrimSpace(doc); doc != "" {
						if _, dup := docs[ts.Name.Name]; !dup {
							docs[ts.Name.Name] = doc
						}
					}
				}
			}
		}
	}
	return docs
}
