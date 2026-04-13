package api

import (
	"testing"
)

func TestRewriteHTMLPaths_SrcHref(t *testing.T) {
	tests := []struct {
		name   string
		html   string
		prefix string
		want   string
	}{
		{
			name:   "rewrite src",
			html:   `<script src="/app.js"></script>`,
			prefix: "/proxy/4646",
			want:   `<script src="/proxy/4646/app.js"></script>`,
		},
		{
			name:   "rewrite href",
			html:   `<link href="/style.css" rel="stylesheet">`,
			prefix: "/proxy/4646",
			want:   `<link href="/proxy/4646/style.css" rel="stylesheet">`,
		},
		{
			name:   "skip protocol-relative",
			html:   `<script src="//cdn.example.com/lib.js"></script>`,
			prefix: "/proxy/4646",
			want:   `<script src="//cdn.example.com/lib.js"></script>`,
		},
		{
			name:   "skip already prefixed",
			html:   `<script src="/proxy/4646/app.js"></script>`,
			prefix: "/proxy/4646",
			want:   `<script src="/proxy/4646/app.js"></script>`,
		},
		{
			name:   "skip relative paths",
			html:   `<img src="image.png">`,
			prefix: "/proxy/4646",
			want:   `<img src="image.png">`,
		},
		{
			name:   "rewrite action",
			html:   `<form action="/search" method="GET">`,
			prefix: "/px",
			want:   `<form action="/px/search" method="GET">`,
		},
		{
			name:   "multiple attributes",
			html:   `<a href="/page"><img src="/img.png"></a>`,
			prefix: "/px",
			want:   `<a href="/px/page"><img src="/px/img.png"></a>`,
		},
		{
			name:   "single quotes",
			html:   `<script src='/app.js'></script>`,
			prefix: "/px",
			want:   `<script src='/px/app.js'></script>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(rewriteHTMLPaths([]byte(tt.html), tt.prefix))
			if got != tt.want {
				t.Errorf("\ngot:  %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestRewriteHTMLPaths_Srcset(t *testing.T) {
	tests := []struct {
		name   string
		html   string
		prefix string
		want   string
	}{
		{
			name:   "single entry",
			html:   `<img srcset="/img.png 2x">`,
			prefix: "/px",
			want:   `<img srcset="/px/img.png 2x">`,
		},
		{
			name:   "multiple entries",
			html:   `<img srcset="/small.png 1x, /large.png 2x">`,
			prefix: "/px",
			want:   `<img srcset="/px/small.png 1x, /px/large.png 2x">`,
		},
		{
			name:   "skip protocol-relative",
			html:   `<img srcset="//cdn.example.com/img.png 1x">`,
			prefix: "/px",
			want:   `<img srcset="//cdn.example.com/img.png 1x">`,
		},
		{
			name:   "skip already prefixed",
			html:   `<img srcset="/px/img.png 1x">`,
			prefix: "/px",
			want:   `<img srcset="/px/img.png 1x">`,
		},
		{
			name:   "mixed entries",
			html:   `<img srcset="/a.png 1x, //cdn.com/b.png 2x, /c.png 3x">`,
			prefix: "/px",
			want:   `<img srcset="/px/a.png 1x, //cdn.com/b.png 2x, /px/c.png 3x">`,
		},
		{
			name:   "with width descriptors",
			html:   `<img srcset="/img.png?w=384 384w, /img.png?w=640 640w">`,
			prefix: "/px",
			want:   `<img srcset="/px/img.png?w=384 384w, /px/img.png?w=640 640w">`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(rewriteHTMLPaths([]byte(tt.html), tt.prefix))
			if got != tt.want {
				t.Errorf("\ngot:  %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestRewriteHTMLPaths_CSSUrl(t *testing.T) {
	tests := []struct {
		name   string
		html   string
		prefix string
		want   string
	}{
		{
			name:   "quoted url in style",
			html:   `<style>@font-face { src: url('/fonts/a.woff2'); }</style>`,
			prefix: "/px",
			want:   `<style>@font-face { src: url('/px/fonts/a.woff2'); }</style>`,
		},
		{
			name:   "double-quoted url",
			html:   `<style>body { background: url("/img/bg.png"); }</style>`,
			prefix: "/px",
			want:   `<style>body { background: url("/px/img/bg.png"); }</style>`,
		},
		{
			name:   "unquoted url",
			html:   `<style>div { background: url(/img/bg.png); }</style>`,
			prefix: "/px",
			want:   `<style>div { background: url(/px/img/bg.png); }</style>`,
		},
		{
			name:   "skip data: urls",
			html:   `<style>div { background: url(data:image/png;base64,abc); }</style>`,
			prefix: "/px",
			want:   `<style>div { background: url(data:image/png;base64,abc); }</style>`,
		},
		{
			name:   "skip https:// urls",
			html:   `<style>div { background: url('https://cdn.com/img.png'); }</style>`,
			prefix: "/px",
			want:   `<style>div { background: url('https://cdn.com/img.png'); }</style>`,
		},
		{
			name:   "multiple url() in one style",
			html:   `<style>@font-face { src: url('/a.woff2'); } body { background: url('/b.png'); }</style>`,
			prefix: "/px",
			want:   `<style>@font-face { src: url('/px/a.woff2'); } body { background: url('/px/b.png'); }</style>`,
		},
		{
			name:   "skip url() outside style tags",
			html:   `<div>url('/not-css')</div>`,
			prefix: "/px",
			want:   `<div>url('/not-css')</div>`,
		},
		{
			name:   "multiple style blocks",
			html:   `<style>a{background:url('/a.png')}</style><p>text</p><style>b{background:url('/b.png')}</style>`,
			prefix: "/px",
			want:   `<style>a{background:url('/px/a.png')}</style><p>text</p><style>b{background:url('/px/b.png')}</style>`,
		},
		{
			name:   "skip already prefixed",
			html:   `<style>div { background: url('/px/img.png'); }</style>`,
			prefix: "/px",
			want:   `<style>div { background: url('/px/img.png'); }</style>`,
		},
		{
			name:   "skip protocol-relative in css",
			html:   `<style>div { background: url('//cdn.com/img.png'); }</style>`,
			prefix: "/px",
			want:   `<style>div { background: url('//cdn.com/img.png'); }</style>`,
		},
		{
			name:   "uppercase STYLE tag",
			html:   `<STYLE>div { background: url('/img.png'); }</STYLE>`,
			prefix: "/px",
			want:   `<STYLE>div { background: url('/px/img.png'); }</STYLE>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(rewriteHTMLPaths([]byte(tt.html), tt.prefix))
			if got != tt.want {
				t.Errorf("\ngot:  %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestRewriteHTMLPaths_RealWorldHTML(t *testing.T) {
	// Simulates a real SSR app HTML with mixed resources.
	html := `<!DOCTYPE html>
<html>
<head>
<style>@font-face{font-family:'App';src:url('/_next/static/media/font.woff2') format('woff2')}</style>
<link href="/_next/static/css/app.css" rel="stylesheet">
</head>
<body>
<img src="/_next/image?url=%2Flogo.png&w=640&q=100"
     srcset="/_next/image?url=%2Flogo.png&w=384&q=100 1x, /_next/image?url=%2Flogo.png&w=640&q=100 2x">
<a href="/about">About</a>
<script src="/_next/static/chunks/main.js"></script>
</body>
</html>`

	prefix := "/api/networks/net-1/nodes/node-1/proxy/3000"

	got := string(rewriteHTMLPaths([]byte(html), prefix))

	checks := []struct {
		desc     string
		contains string
	}{
		{"font url rewritten", `url('` + prefix + `/_next/static/media/font.woff2')`},
		{"css href rewritten", `href="` + prefix + `/_next/static/css/app.css"`},
		{"img src rewritten", `src="` + prefix + `/_next/image?url=%2Flogo.png&w=640&q=100"`},
		{"srcset entries rewritten", prefix + `/_next/image?url=%2Flogo.png&w=384&q=100 1x`},
		{"link href rewritten", `href="` + prefix + `/about"`},
		{"script src rewritten", `src="` + prefix + `/_next/static/chunks/main.js"`},
	}

	for _, c := range checks {
		if !contains(got, c.contains) {
			t.Errorf("%s: expected to contain %q\ngot: %s", c.desc, c.contains, got)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
