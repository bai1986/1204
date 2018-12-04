package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//var chan report parse progress
//var pgmessage chan int
var (
    perror chan string
    kv map[string]interface{}
    rfile *os.File
    parseLine int
    timechan = time.NewTicker(time.Second).C
)

type metaColumn map[string]string
type metaTname map[string]string
type metaColumnList []string

//generate sql "insert into tname(c1,c2...) values()...." according to the struct
type tnameAll struct {
	column     metaColumn
	dbname     metaTname
	columnsort metaColumnList
}

//need trim the ''
const (
	//need trim the ''
	numPattern  = "(int)|(decimal)|(float)|(timestamp)"
	nullPattern = "NULL"
	//need transfor to datetime format
	timePattern = "timestamp"
	header      = "###   @"
	//re := regexp.MustCompile(`(?P<name>[a-zA-Z]+)\s+(?P<age>\d+)\s+(?P<email>\w+@\w+(?:\.\w+)+)`)
	headerStr  = `(?P<pre>###   @%d=)+(?P<value>.*)+\s+(?P<others>/\*.*\*/)`
	header5Str = `(?P<pre>###   @2=)+(?P<value>[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2})+\s+(?P<others>/\*.*\*/)`
)

func main() {
	//flag.Bool("h", false, "this help")
	metaTb := flag.String("meta", "", "table structure file")
	recoverType := flag.String("type", "delete", "choose the recovery type in delete or update")
	binFile := flag.String("logfile", "", "file name was generated by (mysqlbin -vvv --base-out=decode-rows >logfilename)")
	recoverSql := flag.String("out", "reverse.sql", "the resulting recover file")
	flag.Usage = usage
	flag.Parse()
	//if *help {
	//	flag.Usage()
	//}
	centent := readMeta(*metaTb)
	r, err := ParseMeta(centent)
	aline := NewDataStruct(r)
	if err != nil {
		log.Panic(err)
	}

	rfile, err = os.OpenFile(*recoverSql, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Panic(err)
	}
	defer rfile.Close()
	perror = make(chan string)
	if *recoverType == "delete" {
		baseActSql := fmt.Sprintf("insert into %s values", r.dbname["dbname"])
		//baseValueSql := generateValueSql(r)
		//sql := baseActSql + baseValueSql
		sql := baseActSql
		//fmt.Println(sql)
		readParseBinlogFile(*binFile, len(r.columnsort), aline, perror, r, sql, rfile)
		return
	}

	if *recoverType == "update" {
		baseActSql := fmt.Sprintf("update %s set ", r.dbname["dbname"])
		//fmt.Println(baseActSql)
		readParseBinlogFile2(*binFile, len(r.columnsort), aline, perror, r, baseActSql, rfile)
		return
	}

}

func usage() {
	fmt.Fprintf(os.Stderr, `
    Usage:
        ./dml_recovery --type=(delete|update) --meta=t1.txt --logfile=textbinlog1.txt --out=r.sql
Options:
`)
	flag.PrintDefaults()
}

func ConsolePrint(num *int,tchan <-chan time.Time){
    *num += 1
    select{
    case <-timechan:
        fmt.Fprintf(os.Stdout,"\033[32mgenerating: %d lines\033[0m\r",*num)
    default:
    }
}

func NewDataStruct(tb *tnameAll) map[string]interface{} {
	kv = make(map[string]interface{})
	for _, clm := range tb.columnsort {
		kv[clm] = nil
	}
	return kv
}

//init struct
func NewDb() *tnameAll {
	return &tnameAll{
		column:     make(map[string]string),
		dbname:     make(map[string]string),
		columnsort: make([]string, 0),
	}

}

//read all the table structure
func readMeta(mt string) string {
	file, err := ioutil.ReadFile(mt)
	if err != nil {
		log.Fatal("meta file not exists")
	}
	return string(file)

}

//translate map.values() to sql text
func mapToStringSql(basesql string, mp map[string]interface{}, sortline *tnameAll, afile *os.File) {
	checkPattern := regexp.MustCompile(numPattern)
	nullPattern := regexp.MustCompile(nullPattern)
	unixPattern := regexp.MustCompile(timePattern)
	//fmt.Printf(basesql)
	fmt.Fprintf(afile, "%s", basesql)
	//fmt.Printf("(")
	fmt.Fprintf(afile, "%s", "(")
	columnNum := len(sortline.columnsort)
	checkIdx := func(c, l int) bool { return c+1 < l }
	for idx, cc := range sortline.columnsort {
		vv := mp[cc]
		switch vv.(type) {
		case string:
			ss := vv.(string)
			if !strings.HasPrefix(ss, "'") && !strings.HasSuffix(ss, "'") {
				typecl := sortline.column[cc]
				//deal with 5.5 binlog datetime fomat 2018-11-22 22:00:00
				if !checkPattern.MatchString(typecl) && !nullPattern.MatchString(ss) {
					ss = "'" + ss + "'"
					//translate timestamp to datetime and plus  inverted comma;
				} else if unixPattern.MatchString(typecl) {
					sss, _ := strconv.Atoi(ss)
					ss = "'" + time.Unix(int64(sss), 0).Format("2006-01-02 15:04:05") + "'"
				}

			}
			//fmt.Printf("%s", ss)
			fmt.Fprintf(afile, "%s", ss)
			if checkIdx(idx, columnNum) {
				fmt.Fprintf(afile, "%s", ",")
			}
		case int:
			ss := vv.(int)
			fmt.Printf("%d", ss)
			if checkIdx(idx, columnNum) {
				fmt.Fprintf(afile, "%s", ",")
			}
		case float64:
			ss := vv.(float64)
			fmt.Printf("%g", ss)
			if checkIdx(idx, columnNum) {
				fmt.Fprintf(afile, "%s", ",")
			}

		}

	}
	//fmt.Printf(");")
	fmt.Fprintf(afile, "%s\n", ");")
	//fmt.Println()

}

func readParseBinlogFile(binlog string, clm int, dbline map[string]interface{}, errchan chan string, dbc *tnameAll, bsql string, file *os.File) error {

	reheader := regexp.MustCompile(header)
	f, err := os.Open(binlog)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	buf := bufio.NewReader(f)
	cnt := 1
	go func() {
		for {
			msg := <-perror
			fmt.Println(msg)
		}
	}()
	for {
		line, _, err := buf.ReadLine()
		if err != nil {
			if err == io.EOF {
                fmt.Printf("completed, total %d lines!!!\n",parseLine)
				return nil
			}
			return err
		}
		if reheader.Match(line) {
			//fmt.Println("-----match------")
			fheaderStr := fmt.Sprintf(headerStr, cnt)
			//fmt.Println(fheaderStr)
			revalue := regexp.MustCompile(fheaderStr)
			//fmt.Println(string(line)) //
			match := revalue.FindStringSubmatch(string(line))
			if len(match) == 0 {
				revalue5 := regexp.MustCompile(header5Str)
				match = revalue5.FindStringSubmatch(string(line))
			}
			if len(match) == 0 {
				errmsg := fmt.Sprintf("parse line error:\n%s", string(line))
				perror <- errmsg
			}

			if len(match) > 2 {
				dbline[dbc.columnsort[cnt-1]] = match[2]
			} else {
				return errors.New("parse error")
			}
			cnt++
			//fmt.Println(cnt)
			if cnt > clm {
				//fmt.Println(dbline)
				cnt = 1
				mapToStringSql(bsql, dbline, dbc, file)
                ConsolePrint(&parseLine,timechan)
			}

		}

	}

}

//reverse update sql
func readParseBinlogFile2(binlog string, clm int, dbline map[string]interface{}, errchan chan string, dbc *tnameAll, bsql string, file *os.File) error {
	reheader := regexp.MustCompile(header)
	f, err := os.Open(binlog)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	buf := bufio.NewReader(f)
	cnt := 1
	cnt2 := 0
	go func() {
		for {
			msg := <-perror
			fmt.Println(msg)
		}
	}()
	for {
		line, _, err := buf.ReadLine()
		if err != nil {
			if err == io.EOF {
                fmt.Printf("completed, total %d lines!!!\n",parseLine)
				return nil
			}
			return err
		}
		if reheader.Match(line) {
			//fmt.Println("-----match------")
			fheaderStr := fmt.Sprintf(headerStr, cnt)
			//fmt.Println(fheaderStr)
			revalue := regexp.MustCompile(fheaderStr)
			//fmt.Println(string(line)) //
			match := revalue.FindStringSubmatch(string(line))
			if len(match) == 0 {
				revalue5 := regexp.MustCompile(header5Str)
				match = revalue5.FindStringSubmatch(string(line))
			}
			if len(match) == 0 {
				errmsg := fmt.Sprintf("parse line error:\n%s", string(line))
				perror <- errmsg
			}

			if len(match) > 2 {
				dbline[dbc.columnsort[cnt-1]] = match[2]
			} else {
				return errors.New("parse error")
			}
			cnt++
			if cnt > clm {
				cnt = 1
				cnt2 += 1
				mapToStringSql2(bsql, dbline, dbc, cnt2, file)
				if cnt2 > 1 {
					cnt2 = 0
                    ConsolePrint(&parseLine,timechan)
				}

			}

		}

	}

}

//parse the table file into struct
func ParseMeta(tbs string) (*tnameAll, error) {
	//match the table name
	ret := regexp.MustCompile("`.*`")
	// match the column name
	rec := regexp.MustCompile("^`.*`")
	//match column type
	retype := regexp.MustCompile("(?U) (.*) ")
	num := 0
	tc := NewDb()
	//fmt.Println(tc)
	tb := strings.NewReader(tbs)
	buf := bufio.NewReader(tb)
	for {
		line, _, err := buf.ReadLine()
		if err != nil {
			if err == io.EOF {
				//fmt.Println(tc)
				return tc, nil
			}
			return tc, err
		}
		formatLine := strings.TrimSpace(string(line))
		//fmt.Println("-->", formatLine)
		if num == 0 {
			tbname := ret.FindString(formatLine)
			tc.dbname["dbname"] = tbname
			num++
		} else {
			if ok := rec.MatchString(formatLine); ok {
				columnName := rec.FindString(formatLine)
				ctype := retype.FindString(formatLine)
				tc.columnsort = append(tc.columnsort, columnName)
				tc.column[columnName] = ctype
			}
		}
	}

}

func mapToStringSql2(basesql string, mp map[string]interface{}, sortline *tnameAll, dotnum int, afile *os.File) {
	separator := ","
	if dotnum == 2 {
		separator = " and "
		fmt.Fprintf(afile, "%s", " where ")
	} else {
		fmt.Fprintf(afile, "%s", basesql)
	}

	checkPattern := regexp.MustCompile(numPattern)
	nullPattern := regexp.MustCompile(nullPattern)
	unixPattern := regexp.MustCompile(timePattern)

	columnNum := len(sortline.columnsort)
	checkIdx := func(c, l int) bool { return c+1 < l }
	for idx, cc := range sortline.columnsort {
		vv := mp[cc]
		//fmt.Printf("%s=", cc)
		switch vv.(type) {
		case string:
			ss := vv.(string)
			if !strings.HasPrefix(ss, "'") && !strings.HasSuffix(ss, "'") {
				typecl := sortline.column[cc]
				//deal with 5.5 binlog datetime fomat 2018-11-22 22:00:00
				if !checkPattern.MatchString(typecl) && !nullPattern.MatchString(ss) {
					fmt.Fprintf(afile, "%s=", cc)
					ss = "'" + ss + "'"
					//translate timestamp to datetime and plus  inverted comma;
				} else if nullPattern.MatchString(ss) {
					if dotnum > 1 {
						fmt.Fprintf(afile, "%s is ", cc)
					} else {
						fmt.Fprintf(afile, "%s=", cc)
					}

				} else if unixPattern.MatchString(typecl) {
					fmt.Fprintf(afile, "%s=", cc)
					sss, _ := strconv.Atoi(ss)
					ss = "'" + time.Unix(int64(sss), 0).Format("2006-01-02 15:04:05") + "'"
				} else {
					fmt.Fprintf(afile, "%s=", cc)
				}

			} else {
				fmt.Fprintf(afile, "%s=", cc)
			}
			fmt.Fprintf(afile, "%s", ss)
			if checkIdx(idx, columnNum) {
				fmt.Fprintf(afile, "%s", separator)
			}
		case int:
			ss := vv.(int)
			fmt.Fprintf(afile, "%d", ss)
			if checkIdx(idx, columnNum) {
				fmt.Fprintf(afile, "%s", separator)
			}
		case float64:
			ss := vv.(float64)
			fmt.Fprintf(afile, "%g", ss)
			if checkIdx(idx, columnNum) {
				fmt.Fprintf(afile, "%s", separator)
			}

		}

	}
	if dotnum > 1 {
		fmt.Fprintf(afile, "%s\n", ";")
	}
}
