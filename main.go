package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	_ "github.com/mattn/go-sqlite3"
)

type packageIndex struct {
	Name string
	Path string
}

type packageInfo struct {
	Name      string
	Err       error
	IsPackage bool
	Consts    []packageIndex
	Variables []packageIndex
	Funcs     []packageIndex
	Types     []packageIndex
}

func (info *packageInfo) Print() {
	if info.Err != nil {
		fmt.Printf("%s error: %s\n", info.Name, info.Err.Error())
		return
	}
	if !info.IsPackage {
		fmt.Printf("%s is not a package, skip\n", info.Name)
		return
	}
	fmt.Printf("%s contains consts: %#v, funcs: %#v, types: %#v\n", info.Name, info.Consts, info.Funcs, info.Types)
	return
}

func (info *packageInfo) Parse(doc *goquery.Document) {
	wg := &sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		info.ParseType(doc)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		info.ParseFunc(doc)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		info.ParseConstAndVariable(doc)
	}()

	wg.Wait()
}

func (info *packageInfo) ParseType(doc *goquery.Document) {
	doc.Find("h2").Each(func(index int, selection *goquery.Selection) {
		text := selection.Text()
		sign := "type "
		if !strings.HasPrefix(text, sign) {
			return
		}
		name, ok := selection.Attr("id")
		if !ok {
			return
		}
		href, ok := selection.Find("a.permalink").Attr("href")
		if !ok {
			return
		}
		info.Types = append(info.Types, packageIndex{
			Name: name,
			Path: href,
		})
	})
}

func (info *packageInfo) ParseFunc(doc *goquery.Document) {
	doc.Find("h3").Each(func(index int, selection *goquery.Selection) {
		text := selection.Text()
		sign := "func "
		if !strings.HasPrefix(text, sign) {
			return
		}
		name, ok := selection.Attr("id")
		if !ok {
			return
		}
		href, ok := selection.Find("a.permalink").Attr("href")
		if !ok {
			return
		}
		info.Funcs = append(info.Funcs, packageIndex{
			Name: name,
			Path: href,
		})
	})
}

func (info *packageInfo) ParseConstAndVariable(doc *goquery.Document) {
	doc.Find("pre").Each(func(index int, selection *goquery.Selection) {
		text := selection.Text()
		if strings.HasPrefix(text, "const") {
			selection.Find("span").Each(func(index int, selection *goquery.Selection) {
				id, ok := selection.Attr("id")
				if !ok {
					return
				}
				info.Consts = append(info.Consts, packageIndex{
					Name: id,
					Path: "#" + id,
				})
			})
		} else if strings.HasPrefix(text, "var") {
			selection.Find("span").Each(func(index int, selection *goquery.Selection) {
				id, ok := selection.Attr("id")
				if !ok {
					return
				}
				info.Variables = append(info.Variables, packageIndex{
					Name: id,
					Path: "#" + id,
				})
			})
		}
	})
}

func (info *packageInfo) WriteDB(db *sql.DB) (err error) {
	err = info.writeIndexes(db, "Type", info.Types)
	if err != nil {
		return
	}
	err = info.writeIndexes(db, "Function", info.Funcs)
	if err != nil {
		return
	}
	err = info.writeIndexes(db, "Constant", info.Consts)
	if err != nil {
		return
	}
	err = info.writeIndexes(db, "Variable", info.Variables)
	if err != nil {
		return
	}

	return
}

func (info *packageInfo) writeIndexes(db *sql.DB, typeName string, indexes []packageIndex) (err error) {
	sql := `INSERT OR IGNORE INTO searchIndex(name, type, path) VALUES (?,?,?)`
	for _, index := range indexes {
		name := info.Name + "." + index.Name
		p := getDocumentPath(info.Name) + index.Path
		_, err = db.Exec(sql, name, typeName, p)
		if err != nil {
			return
		}
	}

	return
}

var docsetDir string

func main() {
	name := "godoc"
	docsetDir = fmt.Sprintf("%s.docset/Contents", name)
	err := genPlist(name)
	if err != nil {
		fmt.Println(err)
		return
	}

	db, err := createDB()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer db.Close()

	// TODO FIXME
	host := "http://localhost:3000"
	grabLib(host)

	packages, err := getPackages(host)
	if err != nil {
		fmt.Println(err)
		return
	}
	grabPackages(db, host, packages)
}

func getPackages(host string) (packages []string, err error) {
	doc, err := goquery.NewDocument(host + "/pkg/")
	if err != nil {
		return
	}
	doc.Find("div.pkg-dir td.pkg-name a").Each(func(index int, pkg *goquery.Selection) {
		packageName, ok := pkg.Attr("href")
		if !ok {
			return
		}

		// ignore standard packages as there's official go docset already
		domain := strings.Split(packageName, "/")[0]
		if !strings.Contains(domain, ".") {
			return
		}

		packages = append(packages, packageName)
	})
	return
}

func grabPackages(db *sql.DB, host string, packages []string) {
	wg := &sync.WaitGroup{}
	for _, packageName := range packages {
		wg.Add(1)
		go grabPackage(
			wg,
			db,
			strings.TrimRight(packageName, "/"),
			host+"/pkg/"+packageName,
		)
	}

	wg.Wait()
	return
}

func grabPackage(wg *sync.WaitGroup, db *sql.DB, packageName string, url string) {
	defer wg.Done()

	info := &packageInfo{Name: packageName}
	defer info.Print()

	var err error
	defer func() {
		info.Err = err
	}()

	resp, err := http.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(buf))
	if err != nil {
		return
	}

	// skip directories
	pkgDir := doc.Find("div.pkg-dir").First()
	if len(pkgDir.Nodes) > 0 {
		return
	}
	info.IsPackage = true

	documentPath := getDocumentPath(info.Name)
	replaceLinks(doc, documentPath)
	newHTML, err := goquery.OuterHtml(doc.Selection)
	if err != nil {
		return
	}

	err = writeFile(documentPath, strings.NewReader(newHTML))
	if err != nil {
		return
	}

	info.Parse(doc)
	err = info.WriteDB(db)
}

func grabLib(host string) {
	wg := &sync.WaitGroup{}
	wg.Add(1)
	grabDirectory(wg, host, "lib/godoc/")
	wg.Wait()
	return
}

func grabDirectory(wg *sync.WaitGroup, host string, relPath string) {
	defer wg.Done()

	resp, err := http.Get(host + "/" + relPath)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println(err)
		return
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(buf))
	if err != nil {
		fmt.Println(err)
		return
	}
	doc.Find("tbody tr").Each(func(index int, selection *goquery.Selection) {
		// skip ".."
		if len(selection.Children().Nodes) < 2 {
			return
		}
		href, ok := selection.Find("a").First().Attr("href")
		if !ok {
			return
		}

		url := host + "/" + relPath + href
		// download css and js
		if strings.HasSuffix(href, ".css") || strings.HasSuffix(href, ".js") {
			res, err := http.Get(url)
			if err != nil {
				fmt.Println(err)
			}
			defer res.Body.Close()
			err = writeFile(relPath+href, res.Body)
			if err != nil {
				fmt.Println(err)
			}
			return
		}
		// or walk into next directory
		wg.Add(1)
		go grabDirectory(wg, host, url)
	})
	return
}

func genPlist(docsetName string) (err error) {
	err = os.MkdirAll(docsetDir, 0755)
	if err != nil {
		return
	}

	f, err := os.Create(filepath.Join(docsetDir, "Info.plist"))
	if err != nil {
		return
	}
	defer f.Close()
	titleName := strings.ToTitle(docsetName[0:1]) + docsetName[1:]
	f.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>%s</string>
	<key>CFBundleName</key>
	<string>%s</string>
	<key>DocSetPlatformFamily</key>
	<string>%s</string>
	<key>isDashDocset</key>
	<true/>
</dict>
</plist>`,
		docsetName,
		titleName,
		docsetName,
	))
	return
}

func createDB() (db *sql.DB, err error) {
	p := filepath.Join(docsetDir, "Resources/docSet.dsidx")
	err = os.MkdirAll(filepath.Dir(p), 0755)
	if err != nil {
		return
	}
	os.Remove(p)
	db, err = sql.Open("sqlite3", p)
	if err != nil {
		return db, err
	}

	_, err = db.Exec("CREATE TABLE searchIndex(id INTEGER PRIMARY KEY, name TEXT, type TEXT, path TEXT)")
	if err != nil {
		return
	}

	_, err = db.Exec("CREATE UNIQUE INDEX anchor ON searchIndex (name, type, path)")
	if err != nil {
		return
	}

	return
}

func replaceLinks(doc *goquery.Document, documentPath string) {
	dir := path.Dir(documentPath)

	// css
	doc.Find("link").Each(func(index int, selection *goquery.Selection) {
		href, ok := selection.Attr("href")
		if !ok {
			return
		}
		if !strings.HasSuffix(href, ".css") {
			return
		}
		newHref, err := filepath.Rel(dir, strings.TrimLeft(href, "/"))
		if err != nil {
			fmt.Println(err)
			return
		}
		selection.SetAttr("href", newHref)
	})

	// js
	doc.Find("script").Each(func(index int, selection *goquery.Selection) {
		src, ok := selection.Attr("src")
		if !ok {
			return
		}
		if !strings.HasSuffix(src, ".js") {
			return
		}
		newSrc, err := filepath.Rel(dir, strings.TrimLeft(src, "/"))
		if err != nil {
			fmt.Println(err)
			return
		}
		selection.SetAttr("src", newSrc)
	})
}

func writeFile(relPath string, r io.Reader) (err error) {
	p := filepath.Join(docsetDir, "Resources/Documents", relPath)
	err = os.MkdirAll(filepath.Dir(p), 0755)
	if err != nil {
		return
	}

	f, err := os.Create(p)
	if err != nil {
		return
	}
	defer f.Close()

	_, err = io.Copy(f, r)
	return
}

func getDocumentPath(packageName string) string {
	return path.Join("pkg", packageName, "index.html")
}
