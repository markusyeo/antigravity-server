package patches

import (
	_ "embed"
	"regexp"
)

const historyIdentifier = `[$A-Za-z_][$A-Za-z_0-9]*`

var historyProviderRe = regexp.MustCompile(`return(\{getState:\(\)=>` + historyIdentifier + `,onDidChange:` + historyIdentifier + `\.event,dispose:\(\)=>\{` + historyIdentifier + `\.abort\("disposed"\);` + historyIdentifier + `\.dispose\(\)\},requestPageUpdate:async ` + historyIdentifier + `=>` + historyIdentifier + `\(` + historyIdentifier + `\(` + historyIdentifier + `\.AgentStatePageUpdateRequestSchema,\{conversationId:` + historyIdentifier + `,subscriberId:` + historyIdentifier + `,stepPageBounds:` + historyIdentifier + `\}\)\)\})`)

var historyAnchorTimeoutRe = regexp.MustCompile(`(\.current=\{shouldRestore:` + historyIdentifier + `,eltKey:` + historyIdentifier + `,offset:` + historyIdentifier + `\};[^}]*\}\)\)[\s\S]*?\.current=setTimeout\(` + historyIdentifier + `,)5E3(\))`)

var historyManualAnchorRe = regexp.MustCompile(
	`(?s)(?P<prefix>var ` + historyIdentifier +
		`=\(\{sectionRefsMap:(?P<sections>` + historyIdentifier +
		`),requestPageUpdate:` + historyIdentifier +
		`,currentItemsSlice:.*?\{snapshot:(?P<snapshot>` + historyIdentifier +
		`),tryRestoreSnapshot:.*?if\((?P<normalize>` + historyIdentifier +
		`)\(` + historyIdentifier + `,\s*` + historyIdentifier +
		`\)\.startIndex!==.*?let ` + historyIdentifier +
		`=(?P<anchor>` + historyIdentifier + `)\(` + historyIdentifier +
		`\);` + historyIdentifier + `&&` + historyIdentifier +
		`\(.*?var ` + historyIdentifier + `=\(0,` + historyIdentifier +
		`\.useCallback\)\(async ` + historyIdentifier +
		`=>\{.*?var (?P<slice>` + historyIdentifier + `)=` + historyIdentifier +
		`\.current,(?P<total>` + historyIdentifier + `)=` + historyIdentifier +
		`\.current;.*?if\(\((?P<next>` + historyIdentifier + `)=` + historyIdentifier +
		`\)&&!` + historyIdentifier + `\(` + historyIdentifier + `,` + historyIdentifier +
		`\)\)\{)(?P<busy>` + historyIdentifier +
		`)(?P<tail>\.current=!0;try\{.*?finally\{` + historyIdentifier +
		`\(\)\}\},\[)(?P<deps>` + historyIdentifier + `(?:,` + historyIdentifier +
		`)*)(?P<suffix>\]\);return\(0,` + historyIdentifier + `\.useMemo\))`,
)

//go:embed history.js
var historyJavaScript string

//go:embed load_debug.js
var loadDebugJavaScript string

func loadDebugScript(Options) string {
	return `<script id="agy-load-debug">` + loadDebugJavaScript + `</script>`
}

func historyScript(Options) string {
	return `<style>
.agy-history-status { position:sticky; top:8px; z-index:20; height:0; width:max-content; max-width:calc(100% - 32px); margin:0 auto; pointer-events:none; color:var(--foreground); font:inherit; font-size:12px; }
.agy-history-status > span { display:block; padding:6px 12px; line-height:20px; border-radius:6px; background:var(--background); box-shadow:0 2px 8px rgb(0 0 0 / 12%); }
.agy-history-status[data-loading="true"] > span::before { content:""; display:inline-block; width:10px; height:10px; margin-right:8px; border:2px solid currentColor; border-top-color:transparent; border-radius:50%; vertical-align:-2px; animation:agy-history-spin .8s linear infinite; }
@keyframes agy-history-spin { to { transform:rotate(360deg); } }
@media (prefers-reduced-motion:reduce) { .agy-history-status[data-loading="true"] > span::before { animation:none; } }
</style><script id="agy-history">` + historyJavaScript + `</script>`
}
