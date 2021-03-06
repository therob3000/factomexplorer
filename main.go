package main

import (
	//"flag"
	"fmt"
	"github.com/FactomProject/dynrsrc"
	"github.com/FactomProject/gobundle"
	"github.com/FactomProject/gocoding"		
	"os"
	"io/ioutil"	
	"io"
	"log"
	"code.google.com/p/gcfg"
	"github.com/FactomProject/FactomCode/database"	
	"github.com/FactomProject/FactomCode/database/ldb"		
	"github.com/FactomProject/FactomCode/factomapi"	
	"github.com/FactomProject/FactomCode/notaryapi"		
	"strings"
	"time"	
	"encoding/csv"
	"net/http"
	"net/url"	
)

//var portNumber = flag.Int("p", 8087, "Set the port to listen on")
var (
 	logLevel = "DEBUG"
	portNumber int = 8087  	
	applicationName = "factom/client"
	serverAddr = "localhost:8083"	
	ldbpath = "/tmp/client/ldb9"	
	dataStorePath = "/tmp/client/seed/csv"
	refreshInSeconds int = 60
	pagesize = 1000 	
	
	db database.Db // database
	
	//Map to store imported csv files
	clientDataFileMap map[string]string	
	extIDMap map[string]bool	
	
)
func watchError(err error) {
	panic(err)
}

func readError(err error) {
	fmt.Println("error: ", err)
}

func init() {
	
	loadConfigurations()
	factomapi.SetServerAddr(serverAddr)
	
	initDB()
		
	gobundle.Setup.Application.Name = applicationName
	gobundle.Init()
	
	err := dynrsrc.Start(watchError, readError)
	if err != nil { panic(err) }
	
	loadStore()
	loadSettings()
	templates_init()
	serve_init()
	initClientDataFileMap()	
	extIDMap, _ = db.InitializeExternalIDMap()

	// Import data related to new factom blocks created on server
	ticker := time.NewTicker(time.Second * time.Duration(refreshInSeconds)) 
	go func() {
		for _ = range ticker.C {
			downloadAndImportDbRecords()
			RefreshEntries()
		}
	}()		
}

func main() {
	defer func() {
		dynrsrc.Stop()
		server.Close()
	}()
	
	server.Run(fmt.Sprint(":", portNumber))
	
}

func loadConfigurations(){
	cfg := struct {
		App struct{
			PortNumber	int		
			ApplicationName string
			ServerAddr string
			DataStorePath string
			RefreshInSeconds int
			PageSize int
	    }
		Log struct{
	    	LogLevel string
		}
    }{}
	
	wd, err := os.Getwd()
	if err != nil{
		log.Println(err)
	}	
	err = gcfg.ReadFileInto(&cfg, wd+"/client.conf")
	if err != nil{
		log.Println(err)
		log.Println("Client starting with default settings...")
	} else {
	
		//setting the variables by the valued form the config file
		logLevel = cfg.Log.LogLevel	
		applicationName = cfg.App.ApplicationName
		portNumber = cfg.App.PortNumber
		serverAddr = cfg.App.ServerAddr
		dataStorePath = cfg.App.DataStorePath
		refreshInSeconds = cfg.App.RefreshInSeconds
		pagesize = cfg.App.PageSize
	}
	
}

func initDB() {
	
	//init db
	var err error
	db, err = ldb.OpenLevelDB(ldbpath, false)
	
	if err != nil{
		log.Println("err opening db: %v", err)
	}
	
	if db == nil{
		log.Println("Creating new db ...")			
		db, err = ldb.OpenLevelDB(ldbpath, true)
		
		if err!=nil{
			panic(err)
		}		
	}
	
	log.Println("Database started from: " + ldbpath)

}

func downloadAndImportDbRecords() {

 	
	data := url.Values {}	
	data.Set("accept", "json")	
	data.Set("datatype", "filelist")
	data.Set("format", "binary")
	data.Set("password", "opensesame")	
	
	server := fmt.Sprintf(`http://%s/v1`, serverAddr)
	resp, err := http.PostForm(server, data)
	
	if err != nil {
		fmt.Println("Error:%v", err)
		return
	} 	

	contents, _ := ioutil.ReadAll(resp.Body)
	if len(contents) < 5 {
		fmt.Println("The server file list is empty")
		return
	}
	
	serverMap := new (map[string]string)
	reader := gocoding.ReadBytes(contents)
	err = factomapi.SafeUnmarshal(reader, serverMap)	
	
	for key, value := range *serverMap {
		_, existing := clientDataFileMap[key]
		if !existing {
			data := url.Values {}	
			data.Set("accept", "json")	
			data.Set("datatype", "file")
			data.Set("filekey", key)
			data.Set("format", "binary")
			data.Set("password", "opensesame")	
			
			server := fmt.Sprintf(`http://%s/v1`, serverAddr)
			resp, err := http.PostForm(server, data)
			
			if fileNotExists( dataStorePath) {
				os.MkdirAll(dataStorePath, 0755)
			}	
			out, err := os.Create(dataStorePath + "/" + value)
			io.Copy(out, resp.Body)	
			out.Close()					
			
			// import records from the file into db
			file, err := os.Open(dataStorePath + "/" + value)
			if err != nil {panic(err)}
		    
		    reader := csv.NewReader(file) 	
		    //csv header: key, value
		    records, err := reader.ReadAll()	    
		    
		    var ldbMap = make(map[string]string)	
			for _, record := range records {
				ldbMap[record[0]] = record[1]
				if strings.HasPrefix(record[0], "00") { // Entry records start with 00			
			    	binaryKey, _ := notaryapi.DecodeBinary(&record[0])
			    	banaryValue, _ := notaryapi.DecodeBinary(&record[1])	
			    	mapKey := string(binaryKey[1:])			    	

					entry := new (notaryapi.Entry)
					entry.UnmarshalBinary(banaryValue)
					if entry.ExtIDs != nil {
						for i:=0; i<len(entry.ExtIDs); i++{
							mapKey = mapKey + strings.ToLower(string(entry.ExtIDs[i]))
							extIDMap[mapKey] = true
						}
					}
				}
			}	    	
		 	db.InsertAllDBRecords(ldbMap)   
		 	file.Close()
		 	
		 	// add the file to the imported list
		 	clientDataFileMap[key] = value
		}

	}
 	 
			
}

// Initialize the imported file list
func initClientDataFileMap() error {
	clientDataFileMap = make(map[string]string)	
	
	fiList, err := ioutil.ReadDir(dataStorePath)
	if err != nil {
		fmt.Println("Error in initServerDataFileMap:", err.Error())
		return err
	}

	for _, file := range fiList{
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".csv") {
			hash := notaryapi.Sha([]byte(file.Name()))
				
			clientDataFileMap[hash.String()] = file.Name()

		}
	}	
	return nil	
	
}


func fileNotExists(name string) (bool) {
  _, err := os.Stat(name)
  if os.IsNotExist(err) {
    return true
  }
  return err != nil
}