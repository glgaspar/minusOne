package web

import (
	"html/template"
	"io"
	"io/fs"
	"path"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/glgaspar/minusOne/processor"
)

type renderer struct {
	pages     map[string]*template.Template
	fragments *template.Template
}

var templateFuncs = template.FuncMap{
	"videoID": processor.VideoID,
	"inProgress": func(status string) bool {
		return status == "pending" || status == "processing"
	},
	"statusLabel": func(status string) string {
		switch status {
		case "pending":
			return "Queued"
		case "processing":
			return "Processing"
		case "done":
			return "Ready"
		case "failed":
			return "Failed"
		}
		return status
	},
	"ago": func(t time.Time) string {
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return plural(int(d.Minutes()), "minute") + " ago"
		case d < 24*time.Hour:
			return plural(int(d.Hours()), "hour") + " ago"
		case d < 30*24*time.Hour:
			return plural(int(d.Hours()/24), "day") + " ago"
		}
		return t.Format("Jan 2, 2006")
	},
}

func plural(n int, unit string) string {
	if n != 1 {
		unit += "s"
	}
	return strconv.Itoa(n) + " " + unit
}

// Each page is parsed together with the layout and partials, so every page
// can define its own "content" block. Fragments (partials rendered on their
// own for htmx swaps) come from a set without any page.
func newRenderer() *renderer {
	base := template.Must(template.New("").Funcs(templateFuncs).
		ParseFS(assets, "templates/layout.html", "templates/partials/*.html"))

	r := &renderer{pages: map[string]*template.Template{}, fragments: base}
	pages, _ := fs.Glob(assets, "templates/pages/*.html")
	for _, p := range pages {
		r.pages[path.Base(p)] = template.Must(template.Must(base.Clone()).ParseFS(assets, p))
	}
	return r
}

// Render executes a full page when name is a page file (e.g. "catalog.html"),
// otherwise the named fragment (e.g. "song-row").
func (r *renderer) Render(w io.Writer, name string, data any, c echo.Context) error {
	if page, ok := r.pages[name]; ok {
		return page.ExecuteTemplate(w, "layout", data)
	}
	return r.fragments.ExecuteTemplate(w, name, data)
}
