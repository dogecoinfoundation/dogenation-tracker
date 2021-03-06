package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"github.com/rs/cors"
	"os"
	"strconv"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type TX struct {
	TXID          string `json:"txid"`
	OutputNo      int    `json:"output_no"`
	ScriptAsm     string `json:"script_asm"`
	ScriptHex     string `json:"script_hex"`
	Value         string `json:"value"`
	Confirmations int    `json:"confirmations"`
	Time          int    `json:"time"`
}

type Fetcher interface {
	// GetTXChan returns a channel that delivers TXs after the TX with the ID specified
	// use an empty string to get all TXs.
	GetTXChan(string) (chan TX, error)
}

type Store interface {
	GetTotalAmount() (float64, error)
	GetNumOfTXs() (int64, error)
	GetLargestDonation() (TX, error)
	GetRecentDonation() (TX, error)
}

type Info struct {
	Amount  float64
	Number  int64
	Largest TX
	Recent  TX
}

type Cache interface {
	GetInfo() (Info, error)
}

type Config struct {
    AllowedOrigins []string
    AllowedMethods []string
	Wallet string
}

var (
	config = new(Config)
)

func main() {
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		fmt.Println(err)
		return
	}
	err = json.Unmarshal(data, config)
	if err != nil {
		fmt.Println(err)
		return
	}

	f := NewAPIFetcher(config.Wallet)

	db, err := sql.Open("sqlite3", "./txData.db")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, f)
	if err != nil {
		fmt.Println(err)
		return
	}
	cache := NewMemCache(store)

	// This function uses the PORT environment variable
	serveHTTPEndpoints(cache)
}

func serveHTTPEndpoints(c Cache) {
	mux := http.NewServeMux()
	// lessen this repetitive mess later on by making a function that takes in a function operating on info and
	// returning the relevant data and returns a handler
	mux.HandleFunc("/api/count", func(w http.ResponseWriter, r *http.Request) {
		info, err := c.GetInfo()
		if err != nil {
			fmt.Println("Got error while getting API data", err)
			return
		}
		_, err = w.Write([]byte(toJSON(info.Number)))
		if err != nil { // write to the client failed
			fmt.Println("Got error while sending API data", err)
		}
	})
	mux.HandleFunc("/api/total", func(w http.ResponseWriter, r *http.Request) {
		info, err := c.GetInfo()
		if err != nil {
			fmt.Println("Got error while getting API data", err)
			return
		}
		_, err = w.Write([]byte(toJSON(info.Amount)))
		if err != nil { // write to the client failed
			fmt.Println("Got error while sending API data", err)
		}
	})
	mux.HandleFunc("/api/largest", func(w http.ResponseWriter, r *http.Request) {
		info, err := c.GetInfo()
		if err != nil {
			fmt.Println("Got error while getting API data", err)
			return
		}
		_, err = w.Write([]byte(toJSON(info.Largest)))
		if err != nil { // write to the client failed
			fmt.Println("Got error while sending API data", err)
		}
	})
	mux.HandleFunc("/api/recent", func(w http.ResponseWriter, r *http.Request) {
		info, err := c.GetInfo()
		if err != nil {
			fmt.Println("Got error while getting API data", err)
			return
		}
		_, err = w.Write([]byte(toJSON(info.Recent)))
		if err != nil { // write to the client failed
			fmt.Println("Got error while sending API data", err)
		}
	})
	//go func() {
        policy := cors.New(cors.Options{
            AllowedOrigins: config.AllowedOrigins,
            AllowedMethods: config.AllowedMethods,
            AllowCredentials: true,
        })

        httpHandler := policy.Handler(mux)
		err := http.ListenAndServe("0.0.0.0:"+os.Getenv("PORT"), httpHandler)
		if err != nil {
			fmt.Println("Serve error", err)
			os.Exit(127)
		}
	//}()
}

func toJSON(v interface{}) string {
	s, _ := json.MarshalIndent(v, "", "   ")
	return string(s)
}

type apiResults struct {
	Status string `json:"status"`
	Data   struct {
		Network string `json:"network"`
		Address string `json:"address"`
		Txs     []TX   `json:"txs"`
	} `json:"data"`
}

type APIFetcher struct {
	wallet string
}

// NewAPIFetcher returns a new APIFetcher implementing Fetcher
func NewAPIFetcher(wallet string) APIFetcher { // without pointer is fine, it only has a string
	return APIFetcher{wallet: wallet}
}

// GetTXChan returns a channel listing all transactions after the TX with ID afterTXID.
// The channel is closed after all TXs are consumed or an error occurs.
func (a APIFetcher) GetTXChan(afterTXID string) (chan TX, error) {
	r, err := http.Get("https://chain.so/api/v2/get_tx_received/DOGE/" + a.wallet + "/" + afterTXID)
	if err != nil {
		return nil, err
	}
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	api := new(apiResults)
	err = json.Unmarshal(data, api)
	if err != nil {
		return nil, err
	}
	result := make(chan TX, 100)
	go func() {
		for {
			r, err = http.Get("https://chain.so/api/v2/get_tx_received/DOGE/" + a.wallet + "/" + afterTXID)
			if err != nil {
				close(result)
				return
			}
			data, err = ioutil.ReadAll(r.Body)
			if err != nil {
				close(result)
				return
			}
			api = new(apiResults)
			err = json.Unmarshal(data, api)
			if err != nil {
				close(result)
				return
			}
			if len(api.Data.Txs) == 0 {
				close(result)
				return
			}
			for i, v := range api.Data.Txs {
				result <- v
				if i >= len(api.Data.Txs)-1 { // next call needs a fresh page
					afterTXID = v.TXID
				}
			}
		}
	}()
	return result, nil
}

type SQLiteStore struct {
	db   *sql.DB
	lock *sync.RWMutex
}

func NewSQLiteStore(db *sql.DB, f Fetcher) (*SQLiteStore, error) {
	result := new(SQLiteStore)
	result.db = db
	result.lock = new(sync.RWMutex)

	stmt, err := db.Prepare("SELECT TXID from TXs order by id desc limit 1;")
	if err != nil {
		return nil, err
	}
	row := stmt.QueryRow()
	lastTXID := ""
	err = row.Scan(&lastTXID)
	if err != nil && err.Error() != sql.ErrNoRows.Error() {
		return nil, err
	}

	txChan, err := f.GetTXChan(lastTXID) // TODO: replace this with last TX ID in db
	if err != nil {
		return nil, err
	}
	go func() {
		for tx := range txChan {
			result.lock.Lock()
			// TODO: use tx to keep adding stuff to the db
			stmt, err := db.Prepare(`
				INSERT INTO TXs (TXID, OutputNo, ScriptAsm, ScriptHex, Value, Confirmations, Time) VALUES (?,?,?,?,?,?,?)`)
			if err != nil {
				fmt.Println("err", err)
			}
			valueAsFloat, err := strconv.ParseFloat(tx.Value, 64)
			if err != nil {
				fmt.Println("err", err)
			}
			_, err = stmt.Exec(tx.TXID, tx.OutputNo, tx.ScriptAsm, tx.ScriptHex, int64(valueAsFloat*1e8), tx.Confirmations, tx.Time)
			fmt.Println(tx.TXID)
			if err != nil {
				fmt.Println("err", err)
			}
			result.lock.Unlock()
		}
	}()
	return result, nil
}

func (s *SQLiteStore) GetTotalAmount() (float64, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	stmt, err := s.db.Prepare("SELECT SUM(Value) AS TOTAL_AMOUNT FROM TXs;")
	if err != nil {
		return 0, err
	}
	row := stmt.QueryRow()
	sum := new(int64)
	err = row.Scan(sum)
	if err != nil {
		return 0, err
	}
	return float64(*sum)/float64(1e8), nil
}

func (s *SQLiteStore) GetNumOfTXs() (int64, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	stmt, err := s.db.Prepare("SELECT count(id) from TXs;")
	if err != nil {
		return 0, err
	}
	row := stmt.QueryRow()
	count := new(int64)
	err = row.Scan(count)
	if err != nil {
		return 0, err
	}
	return *count, nil
}

type txDB struct {
	TXID          string `json:"txid"`
	OutputNo      int    `json:"output_no"`
	ScriptAsm     string `json:"script_asm"`
	ScriptHex     string `json:"script_hex"`
	Value         int64  `json:"value"`
	Confirmations int    `json:"confirmations"`
	Time          int    `json:"time"`
}

func (s *SQLiteStore) GetLargestDonation() (TX, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()


	stmt, err := s.db.Prepare("SELECT *, MAX(Value) FROM TXs;")
	if err != nil {
		return TX{}, err
	}
	row := stmt.QueryRow()
	//t := new(TX)
	//row.Scan(t)
	dbResult := new(txDB)
	err = row.Scan(new(interface{}), &dbResult.TXID, &dbResult.OutputNo, &dbResult.ScriptAsm, &dbResult.ScriptHex, &dbResult.Value, &dbResult.Confirmations, &dbResult.Time, new(interface{}))
	//fmt.Println(err)
	if err != nil {
		return TX{}, err
	}
	return TX{
		TXID:          dbResult.TXID,
		OutputNo:      dbResult.OutputNo,
		ScriptAsm:     dbResult.ScriptAsm,
		ScriptHex:     dbResult.ScriptHex,
		Value:         strconv.FormatFloat(float64(dbResult.Value) / float64(1e8), 'f', -1, 64),
		Confirmations: dbResult.Confirmations,
		Time:          dbResult.Time,
	}, nil
}

func (s *SQLiteStore) GetRecentDonation() (TX, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	stmt, err := s.db.Prepare("SELECT * from TXs order by id desc limit 1;")
	if err != nil {
		return TX{}, err
	}
	row := stmt.QueryRow()
	dbResult := new(txDB)
	err = row.Scan(new(interface{}), &dbResult.TXID, &dbResult.OutputNo, &dbResult.ScriptAsm, &dbResult.ScriptHex, &dbResult.Value, &dbResult.Confirmations, &dbResult.Time)
	if err != nil {
		return TX{}, err
	}
	return TX{
		TXID:          dbResult.TXID,
		OutputNo:      dbResult.OutputNo,
		ScriptAsm:     dbResult.ScriptAsm,
		ScriptHex:     dbResult.ScriptHex,
		Value:         strconv.FormatFloat(float64(dbResult.Value) / float64(1e8), 'f', -1, 64),
		Confirmations: dbResult.Confirmations,
		Time:          dbResult.Time,
	}, nil
}

// MemCache is a Cache that updates every second
type MemCache struct {
	amount  float64
	number  int64
	largest TX
	recent  TX

	currErr error

	lock *sync.RWMutex
}

func NewMemCache(s Store) *MemCache {
	result := new(MemCache)
	result.lock = new(sync.RWMutex)
	go func() {
		for {
			result.lock.Lock()
			result.amount, result.currErr = s.GetTotalAmount()
			if result.currErr != nil {
				goto End
			}
			result.number, result.currErr = s.GetNumOfTXs()
			if result.currErr != nil {
				goto End
			}
			result.largest, result.currErr = s.GetLargestDonation()
			if result.currErr != nil {
				goto End
			}
			result.recent, result.currErr = s.GetRecentDonation()
			if result.currErr != nil {
				goto End
			}
		End:
			result.lock.Unlock()
			time.Sleep(time.Second)
		}
	}()
	return result
}

func (c *MemCache) GetInfo() (Info, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if c.currErr != nil {
		return Info{}, c.currErr
	}
	return Info{
		Amount:  c.amount,
		Number:  c.number,
		Largest: c.largest,
		Recent:  c.recent,
	}, nil
}
