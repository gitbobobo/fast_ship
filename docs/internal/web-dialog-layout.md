# Web 弹框布局约束（Dialog / AlertDialog）

## 单列必须是 `minmax(0,1fr)`

`ui/dialog.tsx` 的 `DialogContent`、`ui/alert-dialog.tsx` 的 `AlertDialogContent`
都是 `grid` 容器，子节点直接落进同一个隐式列。隐式列默认是 `auto`：它的最小尺寸由
内容的最小内容宽度决定，而 `truncate`（`white-space: nowrap`）的长标题、长
`DialogDescription` 的最小内容宽度就是整段文字宽度。一旦文字比弹框的 `max-w` 宽，
列会被撑到文字宽度，**同一列里的所有子节点（标题、`details`、footer）一起越出弹框
右边界**，而不是各自省略。

因此两个容器都显式写 `grid-cols-[minmax(0,1fr)]`：列的上下限都锁在弹框可用宽度内，
`truncate` 才会真正生效，越界的那个子节点自己省略，不再带着整列跑。

对照证据（INT-114）：弹框 `sm:max-w-lg`（512px），长标题场景下修复前
`DialogContent` 右边界 896px、描述右边界 950px、footer 右边界 966px；修复后描述
右边界回到 880px、footer 与弹框同为 896px。`web/src/components/ui/` 里其它 `grid`
弹框容器改宽度逻辑时沿用同一条约束。

## 覆盖宽度时的写法

需要比默认更宽的弹框用 `className="sm:max-w-xl"` 之类覆盖 `max-w`（`cn`/tailwind-merge
会让后写的生效）；把弹框整体改成别的布局（如 `flex`）时，`grid-cols-*` 会随 `grid`
一起被替换掉，不需要额外处理。
