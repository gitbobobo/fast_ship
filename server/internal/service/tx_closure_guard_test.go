package service

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// INT-97 守护：生产库 SetMaxOpenConns(1)，Transaction 闭包内向池根（r.db /
// s.db / 各 repo 的非 Tx 方法）再借一条连接会永久等待——database/sql 的借连接
// 等待没有超时，busy_timeout 管不到它，整库随之冻结。
//
// 本测试用 AST 扫描 server/ 下全部非测试文件，断言每个 Transaction 内联闭包的
// 词法体内只允许两类调用：
//  1. 事务句柄本身：tx.X(...) / txRepo.X(...)（闭包首参数，无论叫什么名字）；
//  2. 实参显式传事务句柄的调用：repo.SaveTx(tx, ...)、s.emitIssueEventTx(tx, ...)。
//
// XxxTx 命名不构成放行理由——callee 不收句柄就是自己开事务（UploadRunTx），
// 闭包内调它同样自锁。
//
// 包级调用（uuid.NewString、fmt.Sprintf 等）、局部变量的方法调用、纯函数
// 标识符调用放行——它们够不到池根；但对事务发起方句柄（X.Transaction 的 X）、
// 自身方法（s.xxx）与字段接收者（s.projectRepo.xxx、r.db.xxx）的非 Tx 调用、
// 以及把 x.db 当实参传进去，直接判违规。残余盲区：把 r.db 赋给局部变量再调用、
// 包级 var 初始化里的闭包——靠 code review 与 docs/internal/database.md 兜底。
func TestTransactionClosuresOnlyUseTxHandles(t *testing.T) {
	serverRoot := filepath.Join("..", "..")
	var files []string
	err := filepath.Walk(serverRoot, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk server sources: %v", err)
	}

	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			selfName := ""
			if fn.Recv != nil && len(fn.Recv.List) > 0 && len(fn.Recv.List[0].Names) > 0 {
				selfName = fn.Recv.List[0].Names[0].Name
			}
			scanTxClosures(t, fset, file, fn.Body, selfName, funcParamNames(fn))
		}
	}
}

// funcParamNames 收集函数形参名：包级 helper（func helper(s *S)）没有接收者，
// 其形参上的打库字段（s.projectRepo.db 形态）要靠名字启发式拦截。
func funcParamNames(fn *ast.FuncDecl) map[string]struct{} {
	names := map[string]struct{}{}
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, n := range field.Names {
				names[n.Name] = struct{}{}
			}
		}
	}
	return names
}

// 守护自检：用内联源码断言上面那套规则确实拦得住典型漏点、也放得行合法写法。
func TestTxClosureGuardSelfCheck(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		violations int
	}{
		{
			// 事务发起方句柄在闭包内被直接调用（无 selfName 的参数形态）。
			"param-pool-root", `package x
func f(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		return db.Exec("SELECT 1").Error
	})
}`, 1},
		{
			// 自身字段接收者的非 Tx 调用。
			"self-field-repo", `package x
func (s *S) f() {
	s.repo.Transaction(func(tx *gorm.DB) error {
		return s.repo.FindByID("id")
	})
}`, 1},
		{
			// 把池根句柄当实参传进闭包内调用。
			"db-as-arg", `package x
func (s *S) f() {
	s.repo.Transaction(func(tx *gorm.DB) error {
		return helper(s.db, 1)
	})
}`, 1},
		{
			// Tx 命名但不传句柄的 callee（自己开事务的类型）。
			"tx-owning-suffix", `package x
func (s *S) f() {
	s.db.Transaction(func(tx *gorm.DB) error {
		return s.logRepo.UploadRunTx("p", "r")
	})
}`, 1},
		{
			// 包级 helper 形参上的打库字段：s 不是接收者也要拦。
			"param-service-field", `package x
func helper(s *S) {
	s.issueRepo.Transaction(func(tx *gorm.DB) error {
		return s.projectRepo.FindByIDGlobal("p")
	})
}`, 1},
		{
			// 形参直接当接收者且名字像仓库：repo.Find()。
			"param-repo-root", `package x
func f(repo *Repo, db *gorm.DB) {
	db.Transaction(func(tx *gorm.DB) error {
		return repo.Find("x")
	})
}`, 1},
		{
			// 合法：形参上的普通值方法不属于打库通路。
			"param-value-method", `package x
func f(run *Run, db *gorm.DB) {
	db.Transaction(func(tx *gorm.DB) error {
		_ = run.DeadlineAt.Add(d)
		return nil
	})
}`, 0},
		{
			// 合法：句柄调用、传句柄的 Tx 调用、logger 白名单、纯函数。
			"allowed", `package x
func (s *S) f() {
	s.repo.Transaction(func(tx *gorm.DB) error {
		if err := s.repo.SaveTx(tx, v); err != nil {
			return err
		}
		s.logger.With("k", 1).Info("ok")
		return s.emitIssueEventTx(tx, IssueEvent{})
	})
}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			var got []string
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				selfName := ""
				if fn.Recv != nil && len(fn.Recv.List) > 0 && len(fn.Recv.List[0].Names) > 0 {
					selfName = fn.Recv.List[0].Names[0].Name
				}
				got = append(got, txClosureViolations(fset, fn.Body, selfName, funcParamNames(fn))...)
			}
			if len(got) != tc.violations {
				t.Fatalf("expected %d violations, got %d: %v", tc.violations, len(got), got)
			}
		})
	}
}

// scanTxClosures 在函数体内找所有 X.Transaction(func(<tx>) error {...}) 内联闭包，
// 校验闭包词法体内的每个调用。委托形态（Transaction(fc)，fc 是参数/标识符）
// 扫不到闭包体——规则在写出闭包的各调用点上生效。
func scanTxClosures(t *testing.T, fset *token.FileSet, file string, body *ast.BlockStmt, selfName string, paramNames map[string]struct{}) {
	t.Helper()
	for _, v := range txClosureViolations(fset, body, selfName, paramNames) {
		t.Errorf("%s: %s", file, v)
	}
}

// txClosureViolations 返回函数体内所有事务闭包的违规调用（位置+描述）。
func txClosureViolations(fset *token.FileSet, body *ast.BlockStmt, selfName string, paramNames map[string]struct{}) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isTransactionCall(call) || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.FuncLit)
		if !ok {
			return true
		}
		// 记录事务发起方（X.Transaction 的 X）：闭包内再经它调用就是向池根
		// 借第二条连接——db.Transaction(func(tx){ db.Exec(...) }) 形态。
		originName, originField := "", ""
		if root, field := receiverRoot(call.Fun.(*ast.SelectorExpr).X); root != nil {
			originName, originField = root.Name, field
		}
		out = append(out, findTxClosureViolations(fset, lit, txParamName(lit), selfName, paramNames, originName, originField)...)
		return true
	})
	return out
}

func isTransactionCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Transaction"
}

// txParamName 取闭包首参数名（事务句柄）；无参/匿名返回空串，此时只能靠
// Tx 命名判断。
func txParamName(lit *ast.FuncLit) string {
	if lit.Type.Params == nil {
		return ""
	}
	for _, field := range lit.Type.Params.List {
		if len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// findTxClosureViolations 返回闭包体内每处违规调用的位置与描述。
func findTxClosureViolations(fset *token.FileSet, lit *ast.FuncLit, txName, selfName string, paramNames map[string]struct{}, originName, originField string) []string {
	var violations []string
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if allowedInTxClosure(call, txName, selfName, paramNames, originName, originField) {
			return true
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf("%s: %s 在事务闭包内未走事务句柄（INT-97 自锁隐患）", pos, callRepr(call)))
		return true
	})
	return violations
}

func allowedInTxClosure(call *ast.CallExpr, txName, selfName string, paramNames map[string]struct{}, originName, originField string) bool {
	// 把池根句柄（x.db）当实参传进闭包内调用即违规——被调方拿它再借连接。
	for _, arg := range call.Args {
		if containsDBFieldAccess(arg) {
			return false
		}
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		// 内建、包级函数、局部函数值（persistIssue(tx)）都够不到池根；
		// 内部触碰池根的普通标识符调用是已知盲区，由评审规则兜底。
		return true
	case *ast.SelectorExpr:
		if txName != "" && anyArgIsIdent(call, txName) {
			// 实参显式传事务句柄：repo.SaveTx(tx, ...)、s.emitIssueEventTx(tx, ...)。
			return true
		}
		root, field := receiverRoot(fun.X)
		if root == nil {
			// 链式调用中间段（tx.Model(...).Where(...)）与索引接收者
			// （entries[i].Timestamp.Before(...)）：链根若触池根会被单独检查。
			return true
		}
		// tx.X / txRepo.X：接收者就是事务句柄。
		if txName != "" && root.Name == txName {
			return true
		}
		// 事务发起方句柄（db.Transaction 的 db、s.issueRepo 等）：闭包内经它
		// 调用绕过了事务句柄，等同向池根再借一条连接。
		if originName != "" && root.Name == originName && field == originField {
			return false
		}
		// 自身字段接收者（s.projectRepo.X、r.db.X、m.mirror.X）：非 Tx 即违规，
		// 白名单字段（logger 等不打库的）除外。
		if selfName != "" && root.Name == selfName {
			_, ok := txClosureSafeSelfFields[field]
			return ok
		}
		// 外层函数形参（包级 helper 的 s *S 形态）：按打库命名拦截其字段/方法
		// 接收者——param.db.X、param.projectRepo.X、repo.Find——其余值方法
		// （run.DeadlineAt.Add）放行。
		if _, isParam := paramNames[root.Name]; isParam && dangerousTxParamAccess(root.Name, field) {
			return false
		}
		// XxxTx 命名却不传事务句柄：该 callee 要么自己开事务（UploadRunTx、
		// timeoutRunTx——闭包内调用同样自锁），要么命名违规，一律拦截。
		if strings.HasSuffix(fun.Sel.Name, "Tx") {
			return false
		}
		// 包级调用（uuid.NewString）、自身方法（s.xxx）与局部变量方法
		// （run.DeadlineAt.Add）：自身方法已被上面传参规则筛过，剩下放行。
		return true
	default:
		// func(){...}() 立即执行、函数值调用等：其体内调用已在词法范围内被检查。
		return true
	}
}

// dangerousTxParamAccess 名字启发式：root 是外层函数形参时，field 为打库命名
// （db、*Repo、*Repository）即视为池根通路；field 为空（param.Find() 直接方法
// 调用）时按 root 名判断。repo2 这类数字后缀属残余盲区。
func dangerousTxParamAccess(rootName, field string) bool {
	dbish := func(s string) bool {
		l := strings.ToLower(s)
		return l == "db" || strings.HasSuffix(l, "repo") || strings.HasSuffix(l, "repository")
	}
	if field == "" {
		return dbish(rootName)
	}
	return dbish(field)
}

func anyArgIsIdent(call *ast.CallExpr, name string) bool {
	for _, arg := range call.Args {
		if isIdent(arg, name) {
			return true
		}
	}
	return false
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && name != "" && id.Name == name
}

// txClosureSafeSelfFields 是不打库的自身字段名白名单：s.logger.With(...) 这类
// 日志链与 s.storage.Rename(...) 这类文件操作在闭包内可用（都够不到连接池）；
// repo/sink/mirror 等一律走 Tx 命名或显式传句柄。
var txClosureSafeSelfFields = map[string]struct{}{
	"logger":  {},
	"storage": {},
}

// receiverRoot 返回选择器链最左端的标识符与紧邻它的字段名：
// s.projectRepo.FindByID 的接收者链 root=s、field=projectRepo；
// tx.Statement.X 的 root=tx、field=Statement；局部变量/包同理（field 用于
// 区分自身字段与自身方法——s.helper() 的 field 为空串）。
// 接收者是 CallExpr/IndexExpr 等非标识符根时返回 nil。
func receiverRoot(e ast.Expr) (*ast.Ident, string) {
	field := ""
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x, field
		case *ast.SelectorExpr:
			field = x.Sel.Name
			e = x.X
		default:
			return nil, ""
		}
	}
}

// containsDBFieldAccess 判断实参表达式里是否出现 x.db 字段读取（池根句柄）。
func containsDBFieldAccess(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "db" {
			found = true
		}
		return !found
	})
	return found
}

func callRepr(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		var b strings.Builder
		writeExpr(&b, fun.X)
		b.WriteString(".")
		b.WriteString(fun.Sel.Name)
		return b.String() + "(...)"
	case *ast.Ident:
		return fun.Name + "(...)"
	default:
		return "<expr>(...)"
	}
}

func writeExpr(b *strings.Builder, e ast.Expr) {
	switch x := e.(type) {
	case *ast.Ident:
		b.WriteString(x.Name)
	case *ast.SelectorExpr:
		writeExpr(b, x.X)
		b.WriteString(".")
		b.WriteString(x.Sel.Name)
	default:
		b.WriteString("<expr>")
	}
}
