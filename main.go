package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	_ "github.com/mattn/go-sqlite3"
)

const splitter = "=========================================================\n"
const insertSQL = "INSERT OR IGNORE INTO searchIndex(name, type, path) VALUES (?,?,?)"

var silent bool
var docsetDir string

func main() {
	name, icon := parseFlag()
	docsetDir = name + ".docset"

	// icon
	err := writeIcon(icon)
	if err != nil {
		fmt.Println(err)
		return
	}

	// plist
	err = genPlist(name)
	if err != nil {
		fmt.Println(err)
		return
	}

	// DB
	db, err := createDB()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer db.Close()

	// godoc
	cmd, host, err := runGodoc()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() {
		printf("killing godoc on %s\n", host)
		err = cmd.Process.Kill()
		if err != nil {
			fmt.Printf("error killing godoc on %s: %s\n", host, err.Error())
		}
	}()

	// get package list
	packages, err := getPackages(host)
	if err != nil {
		fmt.Println(err)
		return
	}

	// download static resources like css and js
	grabLib(host)

	// prepare
	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer stmt.Close()
	// transaction
	tx, err := db.Begin()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer tx.Commit()

	// download pages and insert DB indexes
	grabPackages(tx.Stmt(stmt), host, packages)
}

func parseFlag() (name string, icon string) {
	silentInput := flag.Bool("silent", false, "Silent mode (only print error)")
	nameInput := flag.String("name", "GoDoc", "Set docset name")
	iconInput := flag.String("icon", "", "Docset icon .png path")

	flag.Parse()
	silent = *silentInput
	name = *nameInput
	icon = *iconInput
	return
}

func writeIcon(p string) (err error) {
	var r io.Reader
	if p == "" {
		var buf []byte
		buf, err = Asset("asset/godoc.png")
		if err != nil {
			return
		}
		r = bytes.NewReader(buf)
	} else {
		var f *os.File
		f, err = os.Open(p)
		if err != nil {
			return
		}
		defer f.Close()
		r = bufio.NewReader(f)
	}

	outputPath := filepath.Join(docsetDir, "icon.png")
	err = os.MkdirAll(filepath.Dir(outputPath), 0755)
	if err != nil {
		return
	}
	w, err := os.Create(outputPath)
	if err != nil {
		return
	}
	_, err = io.Copy(w, r)
	return
}

func createDB() (db *sql.DB, err error) {
	p := filepath.Join(getResourcesDir(), "docSet.dsidx")
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

func runGodoc() (cmd *exec.Cmd, host string, err error) {
	// get a free port
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return
	}
	addr := l.Addr()
	l.Close()
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		err = errors.New("failed to find a free port: " + addr.String())
		return
	}

	// try running godoc on this port
	tryHost := "localhost:" + strconv.Itoa(tcpAddr.Port)
	cmd = exec.Command("godoc", "-http="+tryHost)
	if !silent {
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
	}
	cmd.Env = os.Environ()
	err = cmd.Start()
	if err != nil {
		return
	}
	host = "http://" + tryHost

	// check port is valid now
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		_, err = http.Get(host)
		if err == nil {
			break
		}
	}
	return
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

func grabPackages(stmt *sql.Stmt, host string, packages []string) {
	wg := &sync.WaitGroup{}
	for _, packageName := range packages {
		wg.Add(1)
		go grabPackage(
			wg,
			stmt,
			strings.TrimRight(packageName, "/"),
			host+"/pkg/"+packageName,
		)
	}

	wg.Wait()
	return
}

func grabPackage(wg *sync.WaitGroup, stmt *sql.Stmt, packageName string, url string) {
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
	info.Parse(doc)
	if info.Err != nil {
		return
	}
	if info.IsEmpty() {
		return
	}

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

	err = info.WriteInsert(stmt)
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

	// Avoid visiting entries in godoc html template it self,
	// e.g. entries in /lib/godoc/codewalkdir.html
	if strings.Contains(relPath, "{{") {
		return
	}

	url := host + "/" + relPath
	resp, err := http.Get(url)
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

		// download css and js
		if strings.HasSuffix(href, ".css") || strings.HasSuffix(href, ".js") {
			url := host + "/" + relPath + href
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
		go grabDirectory(wg, host, relPath+href)
	})
	return
}

func genPlist(docsetName string) (err error) {
	contentsDir := getContentsDir()
	err = os.MkdirAll(contentsDir, 0755)
	if err != nil {
		return
	}

	f, err := os.Create(filepath.Join(contentsDir, "Info.plist"))
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
	p := filepath.Join(getResourcesDir(), "Documents", relPath)
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

func getResourcesDir() string {
	return filepath.Join(getContentsDir(), "Resources")
}

func getContentsDir() string {
	return filepath.Join(docsetDir, "Contents")
}

func getDocumentPath(packageName string) string {
	return path.Join("pkg", packageName, "index.html")
}

func printf(format string, a ...interface{}) {
	if !silent {
		fmt.Printf(format, a...)
	}
}
