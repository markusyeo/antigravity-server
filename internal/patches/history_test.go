package patches

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestHistoryProviderLifecycle(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to execute the history provider")
	}
	if out, err := exec.Command("node", "--test", "history_test.cjs").CombinedOutput(); err != nil {
		t.Fatalf("history provider lifecycle: %v\n%s", err, out)
	}
}

func TestManualHistorySnapshotsBeforeRequest(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to execute the native pager")
	}
	patched, _ := Apply(MainJS, []byte(historyManualFixture), Options{})
	setup := `
const assert=require("node:assert/strict");
var trace=[],deps=[];
var React={useCallback:(fn,values)=>{deps=values;return fn},useMemo:fn=>fn()};
var config={},stored={current:{startIndex:200}},length={current:500},busy={current:false};
var sections={current:new Map()},sectionMap=sections.current;
var normalize=(slice,total)=>({startIndex:slice.startIndex<0?total+slice.startIndex:slice.startIndex});
var equal=(a,b)=>a.startIndex===b.startIndex;
var lastSection=map=>({sectionKey:"existing-message"});
var saved;
var save=(predicate,key)=>{saved={predicate,key};trace.push("snapshot")};
var snapshots=()=>({snapshot:save,tryRestoreSnapshot:()=>{}});
var before=value=>value,after=value=>value,record=()=>{};
var acknowledge;
var fetchPage=()=>{trace.push("request");return new Promise(resolve=>{acknowledge=resolve})};
`
	assertions := `
var api=pager({sectionRefsMap:sections,requestPageUpdate:fetchPage,currentItemsSlice:stored.current});
var pending=api.triggerManualUpdate("top");
assert.deepEqual(trace,["snapshot","request"]);
assert.equal(saved.key,"existing-message");
assert.equal(saved.predicate(200),false);
assert.equal(saved.predicate(100),true);
assert.ok(deps.includes(sections)&&deps.includes(save));
assert.equal(busy.current,true);
acknowledge();
pending.then(()=>assert.equal(busy.current,false));
`
	if out, err := exec.Command("node", "-e", setup+string(patched)+assertions).CombinedOutput(); err != nil {
		t.Fatalf("native manual pagination: %v\n%s", err, out)
	}
}

func TestHistoryStatusDoesNotAddMessageHeight(t *testing.T) {
	html := historyScript(Options{})
	wrapper := strings.Split(strings.Split(html, ".agy-history-status {")[1], "}")[0]
	if !strings.Contains(wrapper, "height:0") || strings.Contains(wrapper, "padding:") || strings.Contains(wrapper, "border:") {
		t.Fatal("history status wrapper changes message layout")
	}
	if !bytes.Contains([]byte(html), []byte(`prefers-reduced-motion`)) {
		t.Fatal("history spinner must respect reduced motion")
	}
}

const historyManualFixture = `var pager=({sectionRefsMap:sections,requestPageUpdate:fetchPage,currentItemsSlice:current})=>{var {snapshot:save,tryRestoreSnapshot:restore}=snapshots();function automatic(next,old,total){if(normalize(next,total).startIndex!==normalize(old,total).startIndex){let anchor=lastSection(sectionMap);anchor&&save(index=>index!==old.startIndex,anchor.sectionKey)}}var manual=(0,React.useCallback)(async direction=>{try{var slice=stored.current,total=length.current;var next={startIndex:100};if((direction=next)&&!equal(slice,direction)){busy.current=!0;try{after(await before(fetchPage(direction))),record(slice,direction,total)}finally{after(),busy.current=!1}}}finally{before()}},[config,fetchPage,record,stored,length]);return(0,React.useMemo)(()=>({triggerManualUpdate:manual}),[manual])};`
