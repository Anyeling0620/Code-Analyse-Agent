package codeindex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// extractGo 使用 go/parser + go/ast 精确抽取。
func extractGo(lang string, src []byte) (Facts, bool) {
	f := Facts{Language: lang}
	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, "input.go", src, parser.SkipObjectResolution)
	if file == nil {
		return f, false
	}

	offset := func(p token.Pos) int { return fset.Position(p).Offset }
	lineOf := func(p token.Pos) int { return fset.Position(p).Line }

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			kind := KindFunc
			parent := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = KindMethod
				parent = goBaseType(d.Recv.List[0].Type)
			}
			end := offset(d.Type.End())
			if d.Body != nil {
				end = offset(d.Body.Lbrace)
			}
			sig := flattenSignature(sliceText(src, offset(d.Pos()), end))
			sym := Symbol{
				Name:      name,
				Kind:      kind,
				Parent:    parent,
				Signature: sig,
				LineStart: lineOf(d.Pos()),
				LineEnd:   lineOf(d.End()),
			}
			f.Symbols = append(f.Symbols, sym)

		case *ast.GenDecl:
			switch d.Tok {
			case token.IMPORT:
				for _, spec := range d.Specs {
					is, ok := spec.(*ast.ImportSpec)
					if !ok {
						continue
					}
					path, err := strconv.Unquote(is.Path.Value)
					if err != nil {
						path = strings.TrimSpace(is.Path.Value)
					}
					addEdge(&f, "", path, EdgeImport, ConfidenceExact, lineOf(is.Pos()))
				}
			case token.TYPE:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					sym := Symbol{
						Name:      ts.Name.Name,
						LineStart: lineOf(ts.Pos()),
						LineEnd:   lineOf(ts.End()),
					}
					prefix := "type " + ts.Name.Name
					if ts.Assign.IsValid() {
						prefix += " ="
					}
					switch t := ts.Type.(type) {
					case *ast.StructType:
						sym.Kind = KindStruct
						sym.Signature = prefix + " struct"
						f.Symbols = append(f.Symbols, sym)
						addGoEmbeds(&f, ts.Name.Name, t.Fields, lineOf)
					case *ast.InterfaceType:
						sym.Kind = KindInterface
						sym.Signature = prefix + " interface"
						f.Symbols = append(f.Symbols, sym)
						addGoInterfaceEmbeds(&f, ts.Name.Name, t, lineOf)
					default:
						sym.Kind = KindType
						sym.Signature = flattenSignature(prefix + " " + sliceText(src, offset(ts.Type.Pos()), offset(ts.Type.End())))
						f.Symbols = append(f.Symbols, sym)
					}
				}
			case token.CONST, token.VAR:
				kind := KindConst
				if d.Tok == token.VAR {
					kind = KindVar
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					sig := flattenSignature(sliceText(src, offset(vs.Pos()), offset(vs.End())))
					for _, n := range vs.Names {
						f.Symbols = append(f.Symbols, Symbol{
							Name:      n.Name,
							Kind:      kind,
							Signature: sig,
							LineStart: lineOf(n.Pos()),
							LineEnd:   lineOf(vs.End()),
						})
					}
				}
			}
		}
	}
	return f, true
}

// goBaseType 去掉 *、括号与泛型参数，取接收者基类型名。
func goBaseType(expr ast.Expr) string {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.ParenExpr:
			expr = t.X
		case *ast.IndexExpr:
			expr = t.X
		case *ast.IndexListExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		case *ast.SelectorExpr:
			return t.Sel.Name
		default:
			return ""
		}
	}
}

// addGoEmbeds 记录结构体的匿名字段（嵌入）。
func addGoEmbeds(f *Facts, typeName string, fields *ast.FieldList, lineOf func(token.Pos) int) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		if len(field.Names) != 0 || field.Type == nil {
			continue
		}
		name := goEmbedName(field.Type)
		if name == "" {
			continue
		}
		addEdge(f, typeName, name, EdgeEmbeds, ConfidenceExact, lineOf(field.Pos()))
	}
}

// addGoInterfaceEmbeds 记录接口中的嵌入接口（忽略类型集合里的联合项 ~T / A|B）。
func addGoInterfaceEmbeds(f *Facts, typeName string, iface *ast.InterfaceType, lineOf func(token.Pos) int) {
	if iface.Methods == nil {
		return
	}
	for _, field := range iface.Methods.List {
		if len(field.Names) != 0 || field.Type == nil {
			continue
		}
		var name string
		switch t := field.Type.(type) {
		case *ast.Ident:
			name = t.Name
		case *ast.SelectorExpr:
			name = goExprText(t)
		case *ast.IndexExpr:
			name = goExprText(t.X)
		case *ast.IndexListExpr:
			name = goExprText(t.X)
		}
		if name == "" {
			continue
		}
		addEdge(f, typeName, name, EdgeEmbeds, ConfidenceExact, lineOf(field.Pos()))
	}
}

func goExprText(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		left := goExprText(t.X)
		if left == "" {
			return t.Sel.Name
		}
		return left + "." + t.Sel.Name
	default:
		return ""
	}
}

func goEmbedName(expr ast.Expr) string {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.ParenExpr:
			expr = t.X
		case *ast.IndexExpr:
			return goExprText(t.X)
		case *ast.IndexListExpr:
			return goExprText(t.X)
		default:
			return goExprText(expr)
		}
	}
}
