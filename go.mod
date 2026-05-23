module github.com/DotNetAge/goreact

go 1.26

replace (
	github.com/DotNetAge/gochat => ../gochat
	github.com/DotNetAge/gograph => ../gograph
	github.com/DotNetAge/gorag => ../gorag
)

require (
	github.com/DotNetAge/gochat v0.0.0-00010101000000-000000000000
	github.com/JohannesKaufmann/html-to-markdown v1.6.0
	github.com/google/uuid v1.3.0
	github.com/pkoukk/tiktoken-go v0.1.8
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/PuerkitoBio/goquery v1.9.2 // indirect
	github.com/andybalholm/cascadia v1.3.2 // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	golang.org/x/net v0.25.0 // indirect
)
