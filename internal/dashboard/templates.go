package dashboard

import (
	"embed"
	"html/template"
	"io"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// Render renders a template with the given data
func Render(w io.Writer, templateName string, data interface{}) error {
	// custom functions
	funcMap := template.FuncMap{
		"formatTime": func(t *time.Time) string {
			if t == nil {
				return "-"
			}
			return t.Format("2006-01-02 15:04:05")
		},
		"formatDuration": func(d time.Duration) string {
			return d.Round(time.Second).String()
		},
	}

	tmpl, err := template.New("layout.html").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/"+templateName)
	if err != nil {
		return err
	}

	return tmpl.Execute(w, data)
}
