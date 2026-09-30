package patches

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestInitialConversationPageRequest(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to execute the patched provider")
	}
	const provider = `
var defaults={startIndex:-50},schemas={SliceSchema:{}},create$=(_,value)=>value;
var provider=(id,options)=>({id,...options});
function openConversation(){return provider("thread",{
initialStepsSlice:create$(schemas.SliceSchema,defaults),initialGeneratorMetadatasSlice:create$(schemas.SliceSchema,{startIndex:-1}),
initialExecutorMetadatasSlice:create$(schemas.SliceSchema,{startIndex:0,endIndexExclusive:0})})}
var debug={initialStepsSlice:create$(schemas.SliceSchema,{startIndex:-100})};
console.log(JSON.stringify({conversation:openConversation(),debug}));`
	for _, tc := range []struct {
		name      string
		opts      Options
		wantStart int
	}{
		{"desktop", Options{}, -15},
		{"mobile", Options{MobileUX: true}, -15},
		{"disabled", Options{Disabled: map[string]bool{"conversation-initial-page": true}}, -50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patched, report := Apply(MainJS, []byte(provider), tc.opts)
			for _, result := range report {
				if result.ID == "conversation-initial-page" && result.Status == StatusMissing {
					t.Fatal("initial page anchor did not match")
				}
			}
			out, err := exec.Command("node", "-e", string(patched)).CombinedOutput()
			if err != nil {
				t.Fatalf("execute provider: %v\n%s", err, out)
			}
			var got struct {
				Conversation struct {
					ID         string         `json:"id"`
					Steps      map[string]int `json:"initialStepsSlice"`
					Generators map[string]int `json:"initialGeneratorMetadatasSlice"`
					Executors  map[string]int `json:"initialExecutorMetadatasSlice"`
				} `json:"conversation"`
				Debug struct {
					Steps map[string]int `json:"initialStepsSlice"`
				} `json:"debug"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if got.Conversation.ID != "thread" || got.Conversation.Steps["startIndex"] != tc.wantStart || len(got.Conversation.Steps) != 1 {
				t.Fatalf("unexpected conversation request: %s", out)
			}
			if got.Conversation.Generators["startIndex"] != -1 || got.Conversation.Executors["endIndexExclusive"] != 0 || got.Debug.Steps["startIndex"] != -100 {
				t.Fatalf("unrelated page bounds changed: %s", out)
			}
		})
	}
}
