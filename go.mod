module github.com/DotNetAge/goreact

go 1.26

replace (
	github.com/DotNetAge/gochat => ../gochat
// 	github.com/DotNetAge/gograph => ../gograph
// 	github.com/DotNetAge/gorag => ../gorag
)

require (
	github.com/DotNetAge/gochat v0.2.5
	github.com/JohannesKaufmann/html-to-markdown v1.6.0
	github.com/google/uuid v1.6.0
	github.com/pkoukk/tiktoken-go v0.1.8
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/PuerkitoBio/goquery v1.12.0 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/dlclark/regexp2 v1.12.0 // indirect
	golang.org/x/net v0.55.0 // indirect
)
