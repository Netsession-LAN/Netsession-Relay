package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestThemeSwitch(t *testing.T) {
	e := setup(t)
	c, _ := e.client("view")
	if _, body := get(t, c, e.srv.URL+"/spiele"); strings.Contains(body, "theme98.css") || !strings.Contains(body, `href="/aussehen/98?`) {
		t.Error("default theme is not the brand or the switch is missing")
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for back, want := range map[string]string{"/spiele?ansicht=an": "/spiele?ansicht=an", "//evil.example": "/"} {
		resp, err := c.Get(e.srv.URL + "/aussehen/98?zurueck=" + url.QueryEscape(back))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
			t.Errorf("switch back to %q: %d -> %q", back, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	c.CheckRedirect = nil
	_, body := get(t, c, e.srv.URL+"/spiele")
	if !strings.Contains(body, `data-theme="98"`) || !strings.Contains(body, "theme98.css") || !strings.Contains(body, `href="/aussehen/netsession?`) {
		t.Error("cookie does not switch to the 98 theme")
	}
	if code, _ := get(t, c, e.srv.URL+"/static/theme98.css"); code != 200 {
		t.Errorf("theme98.css = %d", code)
	}
	if code, _ := get(t, c, e.srv.URL+"/static/icons98.svg"); code != 200 {
		t.Errorf("icons98.svg = %d", code)
	}
	if code, _ := get(t, c, e.srv.URL+"/aussehen/xp"); code != 404 {
		t.Errorf("unknown theme = %d", code)
	}
}

// startMenu is the Start menu of a 98 page (the navigation links also exist in the menu bar and on the desktop).
func startMenu(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `<div class="w98-menu">`)
	menu, _, ok2 := strings.Cut(rest, `<label class="w98-task"`)
	if !ok || !ok2 {
		t.Fatal("no Start menu on the 98 page")
	}
	return menu
}

// The 98 desktop: window, taskbar and a Start menu with the navigation of the role, the switches and the logout.
func TestTheme98Desktop(t *testing.T) {
	e := setup(t)
	nav := []string{"/spiele", "/spieler", "/finder", "/netz", "/verlauf", "/system", "/notaus"}
	for role, allowed := range map[string]int{"view": 2, "op": 3, "admin": 6} {
		c, _ := e.client(role)
		// the brand theme has none of the 98 markup
		if _, body := get(t, c, e.srv.URL+"/"); strings.Contains(body, "w98") || strings.Contains(body, "icons98") ||
			!strings.Contains(body, `<footer class="foot">`) || !strings.Contains(body, `<details class="account">`) || strings.Count(body, `id="live"`) != 1 {
			t.Errorf("%s: default theme carries 98 markup or lost its own", role)
		}
		u, _ := url.Parse(e.srv.URL)
		c.Jar.SetCookies(u, []*http.Cookie{{Name: themeCookie, Value: theme98, Path: "/"}})
		_, body := get(t, c, e.srv.URL+"/spiele")
		for _, want := range []string{`<div class="w98-win">`, `<div class="w98-title">`, `<span class="w98-title-text">Spiele – Netsession Relay</span>`,
			`<nav class="w98-taskbar"`, `<details class="w98-start"`, `<label class="w98-task" for="w98-min"`, `<div class="w98-tray">`, `id="engine-state"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: 98 page lacks %s", role, want)
			}
		}
		if strings.Count(body, `id="live"`) != 1 || strings.Count(body, `id="live-text"`) != 1 || strings.Contains(body, `<footer class="foot">`) ||
			strings.Contains(body, `<details class="account">`) {
			t.Errorf("%s: 98 page needs exactly one clock, no brand footer and no account menu", role)
		}
		menu := startMenu(t, body)
		for i, href := range nav {
			has := strings.Contains(menu, `<a href="`+href+`"`)
			want := i < allowed
			if href == "/notaus" {
				want = role != "view" // emergency stop and resume: operators and up
			}
			if has != want {
				t.Errorf("%s: Start menu entry %s = %v, want %v", role, href, has, want)
			}
		}
		for _, want := range []string{`<a href="/"`, `<a href="/spiele" data-app aria-current="page">`, `href="/sprache/en?zurueck=`, `href="/aussehen/netsession?zurueck=`,
			`<form method="post" action="/logout">`, `<input type="hidden" name="csrf" value="`} {
			if !strings.Contains(menu, want) {
				t.Errorf("%s: Start menu lacks %s", role, want)
			}
		}
	}
	// the logout of the Start menu works
	c, csrf := e.client("view")
	resp, err := c.PostForm(e.srv.URL+"/logout", url.Values{"csrf": {csrf}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if code, body := get(t, c, e.srv.URL+"/spiele"); code != 200 || strings.Contains(body, "w98-taskbar") || !strings.Contains(body, `action="/login/local"`) {
		t.Errorf("after logout /spiele = %d, want the login page", code)
	}
}

// getDest is a GET as a browser sends it for a page ("document") or for an iframe ("iframe").
func getDest(t *testing.T, c *http.Client, u, dest string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Sec-Fetch-Dest", dest)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The 98 desktop with windows: a top-level request gets the shell (window manager, taskbar, no page), the iframe
// of a window gets the page without the desktop, and a request without Sec-Fetch-Dest the single-window page.
func TestTheme98Shell(t *testing.T) {
	e := setup(t)
	c, _ := e.client("op")
	u, _ := url.Parse(e.srv.URL)
	// the brand theme never changes with the header
	for _, dest := range []string{"document", "iframe"} {
		if code, body := getDest(t, c, e.srv.URL+"/spiele", dest); code != 200 || strings.Contains(body, "w98") || !strings.Contains(body, `<footer class="foot">`) ||
			!strings.Contains(body, `<main class="wrap"`) {
			t.Errorf("default theme, %s = %d: changed by Sec-Fetch-Dest", dest, code)
		}
	}
	c.Jar.SetCookies(u, []*http.Cookie{{Name: themeCookie, Value: theme98, Path: "/"}})

	code, shell := getDest(t, c, e.srv.URL+"/spiele?ansicht=an", "document")
	if code != 200 {
		t.Fatalf("shell = %d", code)
	}
	for _, want := range []string{`data-w98="shell"`, `<div class="w98-desktop" id="w98-desktop" data-start="/spiele?ansicht=an">`, `/static/w98.js?v=`,
		`<template id="w98-win-tpl">`, `<template id="w98-task-tpl">`, `<div class="w98-tasks" id="w98-tasks">`, `<details class="w98-start"`,
		`<a href="/spiele" data-app aria-current="page">`, `<a href="/finder" data-app>`, `<form method="post" action="/logout">`, `<div class="w98-tray">`,
		`id="stop-form"`, `<dialog class="dialog" id="confirm"`, `<noscript><iframe class="w98-noscript" src="/spiele?ansicht=an"`} {
		if !strings.Contains(shell, want) {
			t.Errorf("shell lacks %s", want)
		}
	}
	for _, not := range []string{`<main`, `<header class="top">`, `id="w98-min"`, `href="/netz"`, `<footer class="foot">`} {
		if strings.Contains(shell, not) {
			t.Errorf("shell contains %s", not)
		}
	}
	if strings.Count(shell, `id="live"`) != 1 {
		t.Error("shell needs exactly one clock")
	}

	code, frame := getDest(t, c, e.srv.URL+"/spiele?ok=Gespeichert", "iframe")
	if code != 200 {
		t.Fatalf("frame = %d", code)
	}
	for _, want := range []string{`data-w98="frame"`, `<main class="wrap"`, `id="conn"`, `id="stop-form"`, `Gespeichert`, `data-live-url="/api/v1/events"`} {
		if !strings.Contains(frame, want) {
			t.Errorf("page in a window lacks %s", want)
		}
	}
	for _, not := range []string{`w98-taskbar`, `w98-start`, `w98-win`, `w98.js`, `<header class="top">`, `<footer class="foot">`, `id="live"`, `action="/logout"`} {
		if strings.Contains(frame, not) {
			t.Errorf("page in a window contains %s", not)
		}
	}

	// no header (plain http, old browsers): the page as a single window that works without script; w98mode.js and
	// w98.js turn it into a window or into the desktop in the browser
	_, body := get(t, c, e.srv.URL+"/spiele")
	for _, want := range []string{`<div class="w98-win">`, `<label class="w98-task" for="w98-min"`, `/static/w98mode.js?v=`, `/static/w98.js?v=`, `<template id="w98-win-tpl">`} {
		if !strings.Contains(body, want) {
			t.Errorf("without Sec-Fetch-Dest the 98 page lacks %s", want)
		}
	}
	if strings.Contains(body, `data-w98=`) || strings.Contains(shell, "w98mode.js") || strings.Contains(frame, "w98mode.js") {
		t.Error("w98mode.js belongs to the page without Sec-Fetch-Dest only")
	}
	// pages may be framed by the own origin only, and the answer depends on the header
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/spiele", nil)
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") ||
		resp.Header.Get("Vary") != "Sec-Fetch-Dest" {
		t.Errorf("framing headers: %v", resp.Header)
	}
	for _, f := range []string{"w98.js", "w98mode.js"} {
		if code, _ := get(t, c, e.srv.URL+"/static/"+f); code != 200 {
			t.Errorf("%s = %d", f, code)
		}
	}
	// a session that ended: the login page is the plain brand page, whatever asks for it (w98.js then leaves the desktop)
	anon, _ := e.client("")
	anon.Jar.SetCookies(u, []*http.Cookie{{Name: themeCookie, Value: theme98, Path: "/"}})
	for _, dest := range []string{"document", "iframe"} {
		if code, body := getDest(t, anon, e.srv.URL+"/spiele", dest); code != 200 || !strings.Contains(body, `action="/login/local"`) || strings.Contains(body, "data-w98") ||
			strings.Contains(body, "w98-desktop") {
			t.Errorf("not signed in, %s = %d: want the plain login page", dest, code)
		}
	}
}
