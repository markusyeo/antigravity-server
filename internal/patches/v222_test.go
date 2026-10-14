package patches

import (
	"strings"
	"testing"
)

func TestMobileConversationRowActionsMatchesV222(t *testing.T) {
	v222 := `className:p("absolute top-0 bottom-0 -right-1 pl-6 flex items-center justify-end gap-0.5 z-10",
E?"hidden":n?"opacity-100":"pointer-fine:opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 focus-within:opacity-100")`

	res, _ := Apply(MainJS, []byte(v222), Options{MobileUX: true})
	out := string(res)

	if strings.Contains(out, `E?"hidden":`) {
		t.Fatalf("v2.22 row actions patch did not apply: %s", out)
	}
	if !strings.Contains(out, `"pointer-fine:opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 focus-within:opacity-100"`) {
		t.Fatalf("v2.22 desktop classes were not preserved: %s", out)
	}
}

func TestMobileTitlebarDeleteModalMatchesV222(t *testing.T) {
	v222 := `g=n1({cascadeId:a,paneId:b,includeRemoveFromSplit:!1});
return g.length>0||r.length>0||w||C.length>0?b_.createElement(b_.Fragment,null,`

	res, _ := Apply(MainJS, []byte(v222), Options{MobileUX: true})
	out := string(res)

	if !strings.Contains(out, `window.__agyDeleteModal?b_.createElement(window.__agyDeleteModal`) {
		t.Fatalf("v2.22 titlebar delete modal patch did not apply: %s", out)
	}
}

func TestFileUploadAcceptAllMatchesV222(t *testing.T) {
	v222 := `ref:t,type:"file",onChange:r,accept:a,
multiple:!0,className:"hidden"`

	res, _ := Apply(MainJS, []byte(v222), Options{})
	out := string(res)

	if !strings.Contains(out, `onChange:r,accept:"*/*",multiple:!0`) {
		t.Fatalf("v2.22 file upload accept all patch did not apply: %s", out)
	}
}

func TestFileUploadLargeFileStreamingMatchesV222(t *testing.T) {
	v222 := `if(S)if(r.size>1048576)f?.("Text file size exceeds 1MB limit"),b("validation_check_failed",
Error("Text file size exceeds 1MB limit"));else{`

	res, _ := Apply(MainJS, []byte(v222), Options{})
	out := string(res)

	if !strings.Contains(out, `if(S)if(r.size>1048576){if(window.__agyUpload){window.__agyUpload([r]);return;}}else{`) {
		t.Fatalf("v2.22 large file streaming patch did not apply: %s", out)
	}
}

func TestSettingsRestoreCustomizationsTabMatchesV222(t *testing.T) {
	v222 := `g=b=>{if(!a)return b;b=new Map(b);b.delete("Customizations");
return b};`

	res, _ := Apply(MainJS, []byte(v222), Options{})
	out := string(res)

	if out != `g=b=>b;` {
		t.Fatalf("v2.22 customizations tab restore patch did not apply: %s", out)
	}
}
