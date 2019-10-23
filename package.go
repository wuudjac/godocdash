package main

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
)

var parseFuncSelectors = []string{
	"h2", // When function does not have any receiver type.
	"h3", // When function has a receiver type.
}

type packageIndex struct {
	Name string
	Path string
}

type packageInfo struct {
	Name      string
	Err       error
	Consts    []packageIndex
	Variables []packageIndex
	Funcs     []packageIndex
	Types     []packageIndex
}

func (info *packageInfo) Print() {
	if info.Err != nil {
		fmt.Printf("\n%s error: %s\n\n"+splitter, info.Name, info.Err.Error())
		return
	}
	if info.IsEmpty() {
		printf("\n%s is not a package, skip\n\n"+splitter, info.Name)
		return
	}
	printf(`
%s contains:
+	const: %+v
+	func: %+v
+	type: %+v

`+splitter,
		info.Name,
		info.Consts,
		info.Funcs,
		info.Types,
	)
	return
}

func (info *packageInfo) IsEmpty() bool {
	return (len(info.Consts) +
		len(info.Variables) +
		len(info.Funcs) +
		len(info.Types)) <= 0
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
	for _, selector := range parseFuncSelectors {
		doc.Find(selector).Each(func(index int, selection *goquery.Selection) {
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

func (info *packageInfo) WriteInsert(stmt *sql.Stmt) (err error) {
	_, err = stmt.Exec(info.Name, "Package", getDocumentPath(info.Name))
	if err != nil {
		return
	}
	err = info.writeIndexes(stmt, "Type", info.Types)
	if err != nil {
		return
	}
	err = info.writeIndexes(stmt, "Function", info.Funcs)
	if err != nil {
		return
	}
	err = info.writeIndexes(stmt, "Constant", info.Consts)
	if err != nil {
		return
	}
	err = info.writeIndexes(stmt, "Variable", info.Variables)
	if err != nil {
		return
	}

	return
}

func (info *packageInfo) writeIndexes(stmt *sql.Stmt, typeName string, indexes []packageIndex) (err error) {
	for _, index := range indexes {
		name := info.Name + "." + index.Name
		p := getDocumentPath(info.Name) + index.Path
		_, err = stmt.Exec(name, typeName, p)
		if err != nil {
			return
		}
	}

	return
}
