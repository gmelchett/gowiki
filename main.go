package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"github.com/microcosm-cc/bluemonday"
)

const (
	LOCAL_TEMPLATE_PATH = "./tmpl/"
	LOCAL_STATIC_PATH   = "./static/"
	LOCAL_DATA_PATH     = "./data/"
)

const (
	VIEW_PATH   = "/view/"
	SAVE_PATH   = "/save/"
	DELETE_PATH = "/delete/"
	EDIT_PATH   = "/edit/"
	PAGES_PATH  = "/pages/"
	STATIC_PATH = "/static/"
)

type joki struct {
	dataPath  string
	templates map[string]*template.Template
	wikiName  string
}

const (
	extension      = ".md"
	frontPageTitle = "Home"
)

// Page represents a page of the wiki
type Page struct {
	fileName string // not part of the viewed page
	Title    string
	Body     []byte
	WikiName string
}

// RenderedPage represents a page that has been rendered to html
type RenderedPage struct {
	Title    string
	Body     template.HTML
	WikiName string
}

func (p *Page) save() error {
	err := ioutil.WriteFile(p.fileName, p.Body, 0600)
	_, isPerr := err.(*os.PathError)
	if err != nil && isPerr {
		// Try to fix path error by making dataPath directory
		err = os.Mkdir(filepath.Dir(p.fileName), 0700)
		if err != nil {
			return err
		}
		log.Printf("Creating %s directory for pages", filepath.Dir(p.fileName))
		return p.save()
	}
	return err
}

// Removes a page
func (p *Page) remove() error {
	return os.Remove(p.fileName)
}

// Renames the page to the new title
func (p *Page) rename(newTitle string) error {
	if !validTitle.MatchString(newTitle) {
		return fmt.Errorf("new title \"%s\" is invalid", newTitle)
	}

	newFileName := filepath.Dir(p.fileName) + newTitle + extension
	if err := os.Rename(p.fileName, newFileName); err == nil {
		p.Title = newTitle
		p.fileName = newFileName
		return nil
	} else {
		return err
	}
}

// Loads a page using its title
func (joki *joki) loadPage(title string) (*Page, error) {
	fileName := joki.dataPath + title + extension
	body, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, err
	}
	return &Page{Title: title, Body: body, fileName: fileName}, nil
}

func (joki *joki) newPage(title string) *Page {
	return &Page{fileName: joki.dataPath + title + extension, Title: title}
}

func (joki *joki) exists(title string) bool {
	filename := joki.dataPath + title + extension
	_, err := os.Stat(filename)
	return !os.IsNotExist(err)
}

func (joki *joki) initTemplates() {
	const (
		templatePath   = "tmpl/"
		templateBase   = "tmpl/layout/base.html"
		templateEnding = ".html"
	)
	templates := []string{"view", "edit", "delete", "new", "pages"}

	for _, tpl := range templates {
		var err error
		joki.templates[tpl], err = template.New(tpl+templateEnding).ParseFiles(templateBase, templatePath+tpl+templateEnding)
		if err != nil {
			log.Fatal("Error loading template:", tpl, err)
		}
	}
}

func (joki *joki) renderTemplate(w http.ResponseWriter, tmpl string, p interface{}) {
	err := joki.templates[tmpl].ExecuteTemplate(w, tmpl+".html", p)
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

func (joki *joki) insertLinks(w io.Writer, node ast.Node, entering bool) (ast.WalkStatus, bool) {

	if _, ok := node.(*ast.Text); !ok {
		return ast.GoToNext, false
	}

	// Interlinking
	withLinks := linkRegex.ReplaceAllFunc(node.AsLeaf().Literal,
		func(link []byte) []byte {
			linkTitle := string(link)
			linkTitle = linkTitle[1 : len(linkTitle)-1]

			linkStr := "<a href=\"" + linkTitle + "\">"

			if joki.exists(linkTitle) {
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

func (joki *joki) renderMarkdown(content []byte) []byte {
	// carriage returns (ASCII 13) are messing things up
	content = bytes.Replace(content, []byte{13}, []byte{}, -1)
	opts := html.RendererOptions{
		Flags:          html.CommonFlags,
		RenderNodeHook: joki.insertLinks,
	}

	return markdown.ToHTML(content, parser.NewWithExtensions(mdExt), html.NewRenderer(opts))
}

func (joki *joki) viewHandler(w http.ResponseWriter, r *http.Request, title string) {
	p, err := joki.loadPage(title)
	if err != nil {
		http.Redirect(w, r, EDIT_PATH+title, http.StatusFound)
		return
	}

	bodyRendered := joki.renderMarkdown(p.Body)

	// Filter output html
	bm := bluemonday.UGCPolicy()
	bm.AllowAttrs("class").Matching(langTags).OnElements("code")  // language tags
	bm.AllowAttrs("class").Matching(colorTags).OnElements("span") // span color selection
	bodyRendered = bm.SanitizeBytes(bodyRendered)

	renderedPage := &RenderedPage{
		Title: p.Title,
		Body:  template.HTML(bodyRendered)}

	joki.renderTemplate(w, "view", renderedPage)
}

// Handles editing pages or creating a new page
func (joki *joki) editHandler(w http.ResponseWriter, r *http.Request, title string) {
	p, err := joki.loadPage(title)
	if err != nil && os.IsNotExist(err) {
		joki.renderTemplate(w, "new", title)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	joki.renderTemplate(w, "edit", p)
}

// Handles saving and moving pages
func (joki *joki) saveHandler(w http.ResponseWriter, r *http.Request, title string) {
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
	p := joki.newPage(title)
	p.Body = []byte(body)
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

	http.Redirect(w, r, VIEW_PATH+title, http.StatusFound)
}

func (joki *joki) deleteHandler(w http.ResponseWriter, r *http.Request, title string) {
	deletionConfirmed := r.FormValue("Confirmed") == "True"
	p := joki.newPage(title)

	if deletionConfirmed {
		err := p.remove()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, VIEW_PATH+frontPageTitle, http.StatusFound)
	} else {
		joki.renderTemplate(w, "delete", p)
	}
}

func (joki *joki) makeHandler(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
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

func (joki *joki) pagesHandler(w http.ResponseWriter, r *http.Request) {
	dataFiles, err := ioutil.ReadDir(joki.dataPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter for page files
	pages := make([]string, 0, len(dataFiles))
	for _, f := range dataFiles {
		fName := f.Name()
		if !f.IsDir() && fName[len(fName)-3:] == extension {
			pages = append(pages, fName[:len(fName)-len(extension)])
		}
	}

	joki.renderTemplate(w, "pages", pages)
}

func main() {

	joki := joki{
		templates: make(map[string]*template.Template),
	}

	var address string

	flag.StringVar(&address, "address", ":8080", "The address to listen to")
	flag.StringVar(&joki.dataPath, "path", LOCAL_DATA_PATH, "Path to the folder that contains the document files")
	flag.StringVar(&joki.wikiName, "wikiname", "JoKi", "Name of wiki")
	flag.Parse()

	joki.initTemplates()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, VIEW_PATH+frontPageTitle, http.StatusFound)
	})

	http.HandleFunc(VIEW_PATH, joki.makeHandler(joki.viewHandler))
	http.HandleFunc(SAVE_PATH, joki.makeHandler(joki.saveHandler))
	http.HandleFunc(DELETE_PATH, joki.makeHandler(joki.deleteHandler))
	http.HandleFunc(EDIT_PATH, joki.makeHandler(joki.editHandler))

	http.HandleFunc(PAGES_PATH, joki.pagesHandler)
	http.Handle(STATIC_PATH, http.StripPrefix(STATIC_PATH, http.FileServer(http.Dir(LOCAL_STATIC_PATH))))

	http.ListenAndServe(address, nil)
}
