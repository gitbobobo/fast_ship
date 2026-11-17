// workflow_census 是 issue「工作流开启/分桶」SQL 口径的唯一出处（INT-239）：
// 凡需判断「开启且未完成」「unset」「待办桶」「进行中桶」或按 workflow_status
// 等值匹配的查询，一律从这里取谓词片段，不得在调用点内联合成 NULL/空串/todo
// 规则。片段只做纯文本拼接，占位符与 args 成对返回，可嵌入 CASE WHEN 与 WHERE。
// 搁置排除（INT-165）不属于本模块：它按项目粒度过滤，继续走 projectNotShelvedSQL。
package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
)

// metaWorkflowCol 带 meta 表别名的 workflow_status 列引用；别名为空时不加前缀，
// 供 EXISTS 子查询（表名即作用域）复用。
func metaWorkflowCol(metaAlias string) string {
	if metaAlias == "" {
		return "workflow_status"
	}
	return metaAlias + ".workflow_status"
}

// WorkflowOpenNotDone 开启且未完成（dashboard 概览计数、分桶分母、积压年龄
// 共用的分母口径）：issue.state=open 且 workflow_status 非 done；meta 缺行时
// workflow_status 为 NULL，同样视为未完成。
func WorkflowOpenNotDone(issueAlias, metaAlias string) (string, []any) {
	meta := metaWorkflowCol(metaAlias)
	sql := issueAlias + ".state = ? AND (" + meta + " IS NULL OR " + meta + " != ?)"
	return sql, []any{model.IssueStateOpen, model.IssueWorkflowStatusDone}
}

// WorkflowUnset 未设置（issue 列表 unset 过滤、项目 unset 计数口径）：meta 缺行
// 或 workflow_status 为空串（列默认值）。用列值判 NULL 而不是 issue_id IS NULL：
// workflow_status 声明了 NOT NULL，缺行时 LEFT JOIN 整列为 NULL，两者在正常数据
// 下等价；但列值写法连「meta 行存在而 status 为 NULL」的脏数据也归入未设置，
// 与 WorkflowOpenNotDone 的分母口径拼起来后，恒等式对该形态同样成立。
func WorkflowUnset(metaAlias string) (string, []any) {
	meta := metaWorkflowCol(metaAlias)
	sql := "(" + meta + " IS NULL OR " + meta + " = ?)"
	return sql, []any{""}
}

// WorkflowTodoBucket 待办桶（dashboard todo_count 口径）：未设置（见 WorkflowUnset）
// 或显式 'todo'。
func WorkflowTodoBucket(metaAlias string) (string, []any) {
	unsetSQL, unsetArgs := WorkflowUnset(metaAlias)
	eqSQL, eqArgs := WorkflowStatusEquals(metaAlias, model.IssueWorkflowStatusTodo)
	return "(" + unsetSQL + " OR " + eqSQL + ")", append(unsetArgs, eqArgs...)
}

// WorkflowInProgressBucket 进行中桶（dashboard in_progress_count 口径）：meta 行
// 存在且 workflow_status 非 ”/'todo'/'done'。显式排除 done，使条件不依赖外层
// 「开启未完成」谓词也独立成立——合法取值下与「外层排 done + 非 ”/todo」逐行等价。
func WorkflowInProgressBucket(metaAlias string) (string, []any) {
	meta := metaWorkflowCol(metaAlias)
	sql := "(" + meta + " IS NOT NULL AND " + meta + " NOT IN (?, ?, ?))"
	return sql, []any{"", model.IssueWorkflowStatusTodo, model.IssueWorkflowStatusDone}
}

// WorkflowStatusEquals 指定状态等值：workflow_status = ?。meta 缺行（NULL）不匹配
// 任何状态，供项目计数与 done 类守卫（批量关闭、GitHub 关闭复核）复用。
func WorkflowStatusEquals(metaAlias string, status model.IssueWorkflowStatus) (string, []any) {
	return metaWorkflowCol(metaAlias) + " = ?", []any{status}
}

// WorkflowOpenWithStatus 开启且处于指定状态：state=open 加等值的组合，供按状态
// 逐一计数的查询（项目 issue_workflow_counts）使用。
func WorkflowOpenWithStatus(issueAlias, metaAlias string, status model.IssueWorkflowStatus) (string, []any) {
	eqSQL, eqArgs := WorkflowStatusEquals(metaAlias, status)
	sql := issueAlias + ".state = ? AND " + eqSQL
	return sql, append([]any{model.IssueStateOpen}, eqArgs...)
}
