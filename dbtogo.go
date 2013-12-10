package main

import (
	"bitbucket.org/pkg/inflect"
	"bytes"
	"database/sql"
	_ "github.com/bmizerany/pq"
	"github.com/codegangsta/cli"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/mattn/go-sqlite3"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"
)

// var (
//   BUILTINS = []string{"break", "default", "func", "interface", "select",
//     "case", " defer", "go", "map", "struct",
//     "chan", " else", " goto", " package", "switch",
//     "const", "fallthrough", "if", "range", "type",
//     "continue", "for", "import", "return", "var"}
// )
var (
	default_template = `package {{.Package}}

// GENERATED BY dbtogo (github.com/kdar/dbtogo); DO NOT EDIT
// ---args: {{join .SafeArgs " "}}

{{define "columns"}}{{$len:=len .}}{{range $i, $v := .}}{{$v.Name|capitalize}}{{if lt $i (sub $len 1)}},{{end}}{{end}}{{end}}
{{define "values"}}{{$len:=len .}}{{range $i, $v := .}}:{{$v.Name|capitalize}}{{if lt $i (sub $len 1)}},{{end}}{{end}}{{end}}

import "database/sql"

var InsertStmts = map[string]string{
{{range $_, $table := .Tables}}  "{{$table.Name}}": "INSERT INTO {{$table.Name}} ({{template "columns" $table.Fields}}) VALUES ({{template "values" $table.Fields}})",
{{end}}}

{{range $_, $table := .Tables}}
type {{$table.Name}} struct {
{{range $_, $field := $table.Fields}}  {{$field.Name|capitalize}} {{$field|typenull}}
{{end}}}
{{end}}`
)

// capitalize the first letter of the string
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// remove all underscores from the string
func nounderscore(s string) string {
	return strings.Replace(s, "_", "", -1)
}

// "go fmt"'s the code
func format(w io.Writer, tabWidth int, code []byte) error {
	fset := token.NewFileSet()

	ast, err := parser.ParseFile(fset, "", code, parser.ParseComments)
	if err != nil {
		return err
	}

	cfg := &printer.Config{Mode: printer.UseSpaces, Tabwidth: tabWidth}
	err = cfg.Fprint(w, fset, ast)
	if err != nil {
		return err
	}

	return nil
}

func render(writer io.Writer, md *Metadata, file string) error {
	funcMap := template.FuncMap{
		"tolower":           strings.ToLower,
		"join":              strings.Join,
		"capitalize":        capitalize,
		"nounderscore":      nounderscore,
		"underscore":        inflect.Underscore,
		"camelize":          inflect.Camelize,
		"camelizedownfirst": inflect.CamelizeDownFirst,
		"pluralize":         inflect.Pluralize,
		"singularize":       inflect.Singularize,
		"tableize":          inflect.Tableize,
		"typeify":           inflect.Typeify,

		"addacronym":     func(a string) string { inflect.AddAcronym(a); return "" },
		"addhuman":       func(a, b string) string { inflect.AddHuman(a, b); return "" },
		"addirregular":   func(a, b string) string { inflect.AddIrregular(a, b); return "" },
		"addplural":      func(a, b string) string { inflect.AddPlural(a, b); return "" },
		"addsingular":    func(a, b string) string { inflect.AddSingular(a, b); return "" },
		"adduncountable": func(a string) string { inflect.AddUncountable(a); return "" },

		"add": func(x, y int) int {
			return x + y
		},
		"sub": func(x, y int) int {
			return x - y
		},

		"typenull": func(f Field) string {
			t := f.Type.String()
			if !strings.HasPrefix(t, "[]") {
				switch t {
				case "bool", "float64", "int64", "string":
					t = "sql.Null" + capitalize(t)
				default:
					t = "*" + t
				}
			}

			return t
		},
		"typepointer": func(f Field) string {
			t := f.Type.String()
			if !strings.HasPrefix(t, "[]") {
				t = "*" + t
			}
			return t
		},
	}

	tplName := file
	tpl := template.New("output").Funcs(funcMap)

	var t *template.Template
	var err error
	if file == "" {
		tplName = "output"
		t, err = tpl.Parse(default_template)
	} else {
		t, err = tpl.ParseFiles(file)
	}

	if err != nil {
		return err
	}

	return t.ExecuteTemplate(writer, tplName, md)
}

func cliAction(cmd string) func(c *cli.Context) {
	return func(c *cli.Context) {
		var err error

		dsn := os.Getenv("DBTOGO_DSN")
		if c.Args().Present() {
			dsn = c.Args().Get(0)
		}

		if dsn == "" {
			log.Fatal("Missing DSN on the command line or the DBTOGO_DSN env")
		}

		db, err := sql.Open(cmd, dsn)
		if err != nil {
			log.Fatal(err)
		}

		var md *Metadata

		switch cmd {
		case "mysql":
			md, err = mysql(db)
		case "postgresql":
			md, err = postgresql(db)
		case "sqlite3":
			md, err = sqlite3(db)
		}

		if err != nil {
			log.Fatal(err)
		}

		md.Package = "model"
		for i, v := range os.Args {
			if i != 0 {
				v = strconv.Quote(v)
			}
			md.Args = append(md.Args, v)
		}

		if c.Args().Present() {
			md.SafeArgs = md.Args[:len(md.Args)-1]
		}

		file := os.Stdout
		switch c.GlobalString("o") {
		case "-", "":
		default:
			file, err = os.Create(c.GlobalString("o"))
			if err != nil {
				log.Fatal(err)
			}

			defer file.Close()
		}

		buffer := &bytes.Buffer{}
		render(buffer, md, c.GlobalString("tpl"))

		if !c.GlobalBool("nofmt") {
			err = format(file, c.GlobalInt("tabwidth"), buffer.Bytes())
			if err != nil {
				log.Fatal(err)
			}
		} else {
			io.Copy(file, buffer)
		}
	}
}

func main() {
	cli.AppHelpTemplate = `NAME:
   {{.Name}} - {{.Usage}}

USAGE:
   {{.Name}} [global options] command [command options] [arguments...]

COMMANDS:
   {{range .Commands}}{{.Name}}{{with .ShortName}}, {{.}}{{end}}{{ "\t" }}{{.Usage}}
   {{end}}
GLOBAL OPTIONS:
   {{range .Flags}}{{.}}
   {{end}}
DATABASES:
   mysql          http://github.com/go-sql-driver/mysql
   postgresql     http://github.com/bmizerany/pq
   sqlite3        http://github.com/mattn/go-sqlite3
`
	cli.CommandHelpTemplate = `NAME:
   {{.Name}} - {{.Usage}}

USAGE:
   dbtogo {{.Name}} [command options] [DSN]

   If you want to omit the DSN on the command line, 
     put your DSN in the DBTOGO_DSN environment variable.

OPTIONS:
   {{range .Flags}}{{.}}
   {{end}}
EXAMPLE:
   dbtogo {{.Name}} {{if eq .Name "mysql"}}mysqluser:pass@tcp(host:port)/db{{end}}{{if eq .Name "postgresql"}}user=pqgotest dbname=pqgotest sslmode=verify-full{{end}}{{if eq .Name "sqlite3"}}./foo.db{{end}}
`

	app := cli.NewApp()
	app.Name = "dbtogo"
	app.Usage = "turns database tables into go code"
	app.Version = "1.0.0"
	app.Flags = []cli.Flag{
		cli.StringFlag{"tpl", "", "template to use in rendering code"},
		cli.BoolFlag{"nofmt", "don't use go fmt to format code"},
		cli.IntFlag{"tabwidth", 4, "tab width for go fmt output"},
		cli.StringFlag{"o", "-", "where to output code. defaults to stdout"},
	}
	// app.Action = func(c *cli.Context) {
	// 	println("Hello friend!")
	// }

	app.Commands = []cli.Command{
		{
			Name:        "mysql",
			Usage:       "connects to a mysql database",
			Description: "",
			Action:      cliAction("mysql"),
		},
		{
			Name:        "sqlite3",
			Usage:       "connects to a sqlite3 database",
			Description: "",
			Action:      cliAction("sqlite3"),
		},
	}

	app.Run(os.Args)
}