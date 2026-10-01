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

//go:embed loading.js
var loadingJavaScript string

func loadingScript(Options) string {
	return `<style>
.agy-loading-shell { position:fixed; inset:0; display:grid; place-content:center; background:#fafafa; color:#242424; font:14px system-ui,sans-serif; }
@media (prefers-color-scheme:dark) { .agy-loading-shell { background:#161616; color:#ededed; } }
.agy-loading-group { display:flex; flex-direction:column; align-items:center; }
.agy-conversation-loading { display:flex; flex-direction:column; align-items:center; gap:12px; max-width:280px; margin:16px auto 0; padding:0 16px; text-align:center; color:var(--foreground); font:inherit; font-size:14px; }
.agy-conversation-loading button { min-height:44px; padding:8px 14px; border:1px solid currentColor; border-radius:6px; background:var(--background); color:inherit; font:inherit; cursor:pointer; }
.agy-conversation-loading button[hidden] { display:none; }
.agy-conversation-loading button:focus-visible { outline:2px solid currentColor; outline-offset:3px; }
</style><script id="agy-loading-feedback">` + loadingJavaScript + `</script>`
}

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
