package main

import (
	"bytes"
	"flag"
	"fmt"
	"gowiki/static"
	"gowiki/tmpl"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"github.com/microcosm-cc/bluemonday"
)

// Config stores the configuration for the wiki that is parsed
// from the command line
type Config struct {
	Address  string // Adress to bind to
	DataPath string // Path to md files
	UseLocal bool   // True if user wants to use local static files e.g. for development
}

const (
	extension      = ".md"
	staticPath     = "static/"
	frontPageTitle = "FrontPage"
)

var dataPath = "data/"

// Page represents a page of the wiki
type Page struct {
	Title string
	Body  []byte
}

// RenderedPage represents a page that has been rendered to html
type RenderedPage struct {
	Title string
	Body  template.HTML
}

func (p *Page) save() error {
	filename := dataPath + p.Title + extension
	err := ioutil.WriteFile(filename, p.Body, 0600)
	_, isPerr := err.(*os.PathError)
	if err != nil && isPerr {
		// Try to fix path error by making dataPath directory
		err = os.Mkdir(dataPath, 0700)
		if err != nil {
			return err
		}
		log.Printf("Creating %s directory for pages", dataPath)
		return p.save()
	} else if err != nil {
		return err
	}
	return nil
}

// Removes a page
func (p *Page) remove() error {
	filename := dataPath + p.Title + extension
	return os.Remove(filename)
}

// Renames the page to the new title
func (p *Page) rename(newTitle string) error {
	if !validTitle.MatchString(newTitle) {
		return fmt.Errorf("new title \"%s\" is invalid", newTitle)
	}

	filename := dataPath + p.Title + extension
	newFileanme := dataPath + newTitle + extension
	err := os.Rename(filename, newFileanme)
	if err != nil {
		return err
	}

	p.Title = newTitle
	return nil
}

// Loads a page using its title
func loadPage(title string) (*Page, error) {
	filename := dataPath + title + extension
	body, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	return &Page{Title: title, Body: body}, nil
}

func exists(title string) bool {
	filename := dataPath + title + extension
	_, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	} else {
		return true
	}
}

var templateMap map[string]*template.Template

func initTemplates(useLocal bool) error {
	const (
		templatePath   = "/tmpl/"
		templateBase   = "/tmpl/layout/base.html"
		templateEnding = ".html"
	)
	templates := []string{"view", "edit", "delete", "new", "pages"}

	templateMap = make(map[string]*template.Template)
	for _, tpl := range templates {
		newTmpl := template.New(tpl + templateEnding)
		// First load base template
		_, err := newTmpl.Parse(tmpl.FSMustString(useLocal, templateBase))
		if err != nil {
			return err
		}
		// Then add the specific one over it
		_, err = newTmpl.Parse(tmpl.FSMustString(useLocal, templatePath+tpl+templateEnding))
		if err != nil {
			return err
		}

		templateMap[tpl] = newTmpl
	}

	return nil
}

func renderTemplate(w http.ResponseWriter, tmpl string, p interface{}) {
	err := templateMap[tmpl].ExecuteTemplate(w, tmpl+".html", p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

var validTitle = regexp.MustCompile(`^([a-zA-Z0-9]+)$`)
var validPath = regexp.MustCompile(`^/(((view|delete)/([a-zA-Z0-9]+))|((edit|save)/([a-zA-Z0-9]*)))$`)
var linkRegex = regexp.MustCompile(`\[([a-zA-Z0-9]+)\]`)
var langTags = regexp.MustCompile("^language-[a-zA-Z0-9]+$")
var colorTags = regexp.MustCompile("^has-text-[a-zA-Z0-9-]+$")

const mdExt parser.Extensions = parser.Tables | parser.FencedCode |
	parser.Autolink | parser.Strikethrough | parser.SpaceHeadings |
	parser.NoEmptyLineBeforeBlock | parser.HeadingIDs | parser.AutoHeadingIDs |
	parser.BackslashLineBreak | parser.DefinitionLists | parser.MathJax |
	parser.SuperSubscript | parser.Footnotes

func insertLinks(w io.Writer, node ast.Node, entering bool) (ast.WalkStatus, bool) {

	if _, ok := node.(*ast.Text); !ok {
		return ast.GoToNext, false
	}

	// Interlinking
	withLinks := linkRegex.ReplaceAllFunc(node.AsLeaf().Literal,
		func(link []byte) []byte {
			linkTitle := string(link)
			linkTitle = linkTitle[1 : len(linkTitle)-1]

			linkStr := "<a href=\"" + linkTitle + "\">"

			if exists(linkTitle) {
				linkStr += linkTitle
			} else {
				linkStr += "<span class=\"has-text-danger\">" + linkTitle + " <sup>(No such page)</sup></span>"
			}

			linkStr += "</a>"
			return []byte(linkStr)
		})

	w.Write(withLinks)

	return ast.GoToNext, true
}

func renderMarkdown(content []byte) []byte {
	// carriage returns (ASCII 13) are messing things up
	content = bytes.Replace(content, []byte{13}, []byte{}, -1)
	opts := html.RendererOptions{
		Flags:          html.CommonFlags,
		RenderNodeHook: insertLinks,
	}

	content = markdown.ToHTML(content, parser.NewWithExtensions(mdExt), html.NewRenderer(opts))
	return content
}

func viewHandler(w http.ResponseWriter, r *http.Request, title string) {
	p, err := loadPage(title)
	if err != nil {
		http.Redirect(w, r, "/edit/"+title, http.StatusFound)
		return
	}

	bodyRendered := renderMarkdown(p.Body)

	// Filter output html
	bm := bluemonday.UGCPolicy()
	bm.AllowAttrs("class").Matching(langTags).OnElements("code")  // language tags
	bm.AllowAttrs("class").Matching(colorTags).OnElements("span") // span color selection
	bodyRendered = bm.SanitizeBytes(bodyRendered)

	renderedPage := &RenderedPage{
		Title: p.Title,
		Body:  template.HTML(bodyRendered)}

	renderTemplate(w, "view", renderedPage)
}

// Handles editing pages or creating a new page
func editHandler(w http.ResponseWriter, r *http.Request, title string) {
	p, err := loadPage(title)
	if err != nil && os.IsNotExist(err) {
		renderTemplate(w, "new", title)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "edit", p)
}

// Handles saving and moving pages
func saveHandler(w http.ResponseWriter, r *http.Request, title string) {
	body := strings.Replace(r.FormValue("body"), "\r", "", -1)
	newTitle := r.FormValue("title")
	if title == "" {
		title = newTitle // use form title for creating a new page
	}

	// Check for valid title before saving
	if !validTitle.MatchString(title) {
		http.Error(w, "Title name is invalid: "+title, http.StatusBadRequest)
		return
	}

	// Create or Overwrite page
	p := &Page{Title: title, Body: []byte(body)}
	err := p.save()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Rename/Move page if title was changed
	if newTitle != title {
		err := p.rename(newTitle)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		title = newTitle
	}

	http.Redirect(w, r, "/view/"+title, http.StatusFound)
}

func deleteHandler(w http.ResponseWriter, r *http.Request, title string) {
	deletionConfirmed := r.FormValue("Confirmed") == "True"
	p := Page{Title: title}

	if deletionConfirmed {
		err := p.remove()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/view/"+frontPageTitle, http.StatusFound)
	} else {
		renderTemplate(w, "delete", p)
	}
}

func makeHandler(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := validPath.FindStringSubmatch(r.URL.Path)
		// log.Printf("%#v\n", m)
		if m == nil {
			http.NotFound(w, r)
			return
		}

		// m[4]+m[7] is the content of the capture groups that eventually contain
		// the page title in /edit/title and /save/title but always contain
		// the page title in /view/title and /delete/title
		fn(w, r, m[4]+m[7])
	}
}

func pagesHandler(w http.ResponseWriter, r *http.Request) {
	dataFiles, err := ioutil.ReadDir(dataPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter for page files
	pages := make([]string, 0, len(dataFiles))
	for _, f := range dataFiles {
		fName := f.Name()
		if !f.IsDir() && fName[len(fName)-3:] == extension {
			pages = append(pages, fName[:len(fName)-3])
		}
	}

	renderTemplate(w, "pages", pages)
}

func listen(conf Config) error {
	// TODO: Refactor model
	dataPath = conf.DataPath

	err := initTemplates(conf.UseLocal)
	if err != nil {
		log.Fatal("error initializing templates:", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/view/"+frontPageTitle, http.StatusFound)
	})

	// Operations on pages
	http.HandleFunc("/view/", makeHandler(viewHandler))
	http.HandleFunc("/save/", makeHandler(saveHandler))
	http.HandleFunc("/delete/", makeHandler(deleteHandler))
	http.HandleFunc("/edit/", makeHandler(editHandler))

	// View list of all pages
	http.HandleFunc("/pages", pagesHandler)
	http.Handle("/static/",
		http.FileServer(static.FS(conf.UseLocal)))

	return http.ListenAndServe(conf.Address, nil)
}

func parseConfig() Config {
	address := flag.String("address",
		":8080", "The address to listen to")
	dataPath := flag.String("path",
		"data/", "Path to the folder that contains the document files")
	useLocal := flag.Bool("local", false,
		"Use local static files and templates instead of embedded ones.")
	flag.Parse()
	return Config{Address: *address, DataPath: *dataPath, UseLocal: *useLocal}
}

func main() {
	config := parseConfig()
	log.Fatal(listen(config))
}
