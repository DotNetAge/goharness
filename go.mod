module github.com/DotNetAge/goreact

go 1.26

replace (
	github.com/DotNetAge/gochat => ../gochat
	github.com/DotNetAge/gograph => ../gograph
	github.com/DotNetAge/gorag => ../gorag
)

require (
	github.com/DotNetAge/gochat v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.3.0
	github.com/pkoukk/tiktoken-go v0.1.8
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/JohannesKaufmann/html-to-markdown v1.6.0 // indirect
	github.com/PuerkitoBio/goquery v1.9.2 // indirect
	github.com/andybalholm/cascadia v1.3.2 // indirect
	github.com/chromedp/cdproto v0.0.0-20260321001828-e3e3800016bc // indirect
	github.com/chromedp/chromedp v0.15.1 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260214004413-d219187c3433 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
)
