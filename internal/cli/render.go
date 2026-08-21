package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// PrintTable 用 tabwriter 输出对齐表格(首行表头)。
func PrintTable(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

// PrintJSON 输出缩进的 JSON(--json 模式)。
func PrintJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// Truncate 超长字符串截断加省略号,避免表格单元格撑爆终端。
func Truncate(s string, n int) string {
	if n <= 0 || len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// FmtTime 把 RFC3339 时间串转成易读的本地格式;解析失败原样返回。
func FmtTime(s string) string {
	if s == "" {
		return "-"
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Local().Format("2006-01-02 15:04:05")
		}
	}
	return s
}

// FmtSize 人类可读的文件大小。
func FmtSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// StatusMark 状态对应的展示标记(成功/失败一目了然)。
func StatusMark(status string) string {
	switch status {
	case "success":
		return "success ✓"
	case "failed":
		return "failed ✗"
	default:
		return status
	}
}
