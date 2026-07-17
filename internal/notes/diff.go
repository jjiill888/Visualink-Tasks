package notes

import "strings"

// 历史版本行级 diff(对比页用):最长公共子序列,零依赖。
// 先掐掉公共前后缀再做 DP;掐剩的中段仍过大时退化为整段替换(极端长文保护)。

// diffLine 对比页的一行。Op ∈ same/add/del/skip,skip 是折叠标记(N=被折叠行数)。
type diffLine struct {
	Op   string
	Text string
	N    int
}

// diffCtx 改动上下保留的未改动行数,更长的连续未改动段折叠成 skip 标记。
const diffCtx = 3

// splitLines 空文本视为零行(而不是一个空行),否则空版本会 diff 出一条幽灵空行。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// diffLines 计算 old→new 的行级 diff,返回折叠后的行序列与增/删行数。
func diffLines(oldText, newText string) (lines []diffLine, adds, dels int) {
	a, b := splitLines(oldText), splitLines(newText)

	// 公共前缀/后缀(后缀不与前缀重叠)——真实编辑通常只动中间一小段
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]

	var mid []diffLine
	if len(ma)*len(mb) > 4_000_000 {
		// DP 表会太大:中段整体视为删旧增新
		for _, l := range ma {
			mid = append(mid, diffLine{Op: "del", Text: l})
		}
		for _, l := range mb {
			mid = append(mid, diffLine{Op: "add", Text: l})
		}
	} else {
		mid = lcsDiff(ma, mb)
	}

	full := make([]diffLine, 0, p+len(mid)+s)
	for _, l := range a[:p] {
		full = append(full, diffLine{Op: "same", Text: l})
	}
	full = append(full, mid...)
	for _, l := range a[len(a)-s:] {
		full = append(full, diffLine{Op: "same", Text: l})
	}
	for _, l := range full {
		switch l.Op {
		case "add":
			adds++
		case "del":
			dels++
		}
	}
	return collapseSame(full), adds, dels
}

// lcsDiff 标准 LCS 回溯;dp[i][j] = a[i:] 与 b[j:] 的 LCS 长度。
// 不匹配时先出 del 再出 add,与常见 diff 工具的呈现顺序一致。
func lcsDiff(a, b []string) []diffLine {
	m, n := len(a), len(b)
	dp := make([][]int32, m+1)
	for i := range dp {
		dp[i] = make([]int32, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	out := make([]diffLine, 0, m+n)
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{Op: "same", Text: a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, diffLine{Op: "del", Text: a[i]})
			i++
		default:
			out = append(out, diffLine{Op: "add", Text: b[j]})
			j++
		}
	}
	for ; i < m; i++ {
		out = append(out, diffLine{Op: "del", Text: a[i]})
	}
	for ; j < n; j++ {
		out = append(out, diffLine{Op: "add", Text: b[j]})
	}
	return out
}

// collapseSame 把长的连续未改动段折叠成 skip 标记,改动上下各留 diffCtx 行;
// 文首段只留贴近改动的尾部,文末段只留贴近改动的头部。
func collapseSame(in []diffLine) []diffLine {
	var out []diffLine
	i := 0
	for i < len(in) {
		if in[i].Op != "same" {
			out = append(out, in[i])
			i++
			continue
		}
		j := i
		for j < len(in) && in[j].Op == "same" {
			j++
		}
		head, tail := diffCtx, diffCtx
		if i == 0 {
			head = 0
		}
		if j == len(in) {
			tail = 0
		}
		if run := j - i; run > head+tail+1 {
			out = append(out, in[i:i+head]...)
			out = append(out, diffLine{Op: "skip", N: run - head - tail})
			out = append(out, in[j-tail:j]...)
		} else {
			out = append(out, in[i:j]...)
		}
		i = j
	}
	return out
}
