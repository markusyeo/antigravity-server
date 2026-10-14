package patches

import (
	"strings"
	"testing"
)

func TestSkipOnboardingMatchesV223(t *testing.T) {
	v223 := `var h=function(){var a=c.hasOnboardingScreens&&d!==2&&e({to:"/onboarding",search:f,replace:!0,throw:!0});return a}`
	res, _ := Apply(MainJS, []byte(v223), Options{MobileUX: true})
	out := string(res)
	if !strings.Contains(out, `return null`) {
		t.Fatalf("v2.23 skip-onboarding did not apply: %s", out)
	}
}

func TestMobileEnterNewlineMatchesV223(t *testing.T) {
	v223 := `e.registerCommand(KEY_ENTER_COMMAND,a=>{if(!a)return!1;var b=a.shiftKey&&(a.metaKey||a.ctrlKey);if(a.shiftKey&&!b)return!1;a.preventDefault();return c(),!0})`
	res, _ := Apply(MainJS, []byte(v223), Options{MobileUX: true})
	out := string(res)
	if !strings.Contains(out, `var b=a.shiftKey&&(a.metaKey||a.ctrlKey);if(a.shiftKey&&!b)return!1;var _t=`) {
		t.Fatalf("v2.23 mobile-enter-newline did not apply or dropped shift+cmd logic: %s", out)
	}
}

func TestMobileProjectSortOrderMatchesV223(t *testing.T) {
	v223 := `if(a.length===0)return J2e(c,d,e,f,g).map(H2e);`
	res, _ := Apply(MainJS, []byte(v223), Options{MobileUX: true})
	out := string(res)
	if !strings.Contains(out, `let _raw=J2e(c,d,e,f,g).map(H2e);`) {
		t.Fatalf("v2.23 mobile-project-sort-order did not apply: %s", out)
	}
}

func TestAuthServiceLoginRedirectMatchesV223(t *testing.T) {
	v223 := `class AuthService{async loginWithRedirect(a){var b=await this.getAuthUrl(a);window.open(b)}async showLoginFlow(){return this.loginWithRedirect()}}`
	res, _ := Apply(MainJS, []byte(v223), Options{MobileUX: true})
	out := string(res)
	if !strings.Contains(out, `async loginWithRedirect(a){window.location.href="/__agy/signin";return;`) {
		t.Fatalf("v2.23 auth-service-login-redirect did not apply: %s", out)
	}
	if !strings.Contains(out, `async showLoginFlow(){window.location.href="/__agy/signin";return;`) {
		t.Fatalf("v2.23 auth-service-show-login-flow did not apply: %s", out)
	}
}

func TestAuthServiceStubRedirectMatchesV223(t *testing.T) {
	v223 := `var stub={loginWithRedirect:async()=>{console.log("[AuthService] loginWithRedirect")},showLoginFlow:async()=>{console.log("[AuthService] showLoginFlow")}};`
	res, _ := Apply(MainJS, []byte(v223), Options{MobileUX: true})
	out := string(res)
	if !strings.Contains(out, `loginWithRedirect:async()=>{window.location.href="/__agy/signin"},showLoginFlow:async()=>{window.location.href="/__agy/signin"}`) {
		t.Fatalf("v2.23 auth-service-stub-login-redirect did not apply: %s", out)
	}
}
