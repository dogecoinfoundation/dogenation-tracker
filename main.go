package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
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
	Wallet string
}

var (
	c = new(Config)
)

func main() {
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		fmt.Println(err)
		return
	}
	err = json.Unmarshal(data, c)
	if err != nil {
		fmt.Println(err)
		return
	}

	f := NewAPIFetcher(c.Wallet)

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

	//fmt.Println(cache.GetInfo())

	// This function uses the PORT environment variable
	serveHTTPEndpoints(cache)
}

func serveHTTPEndpoints(c Cache) {
	mux := http.NewServeMux()
	// lessen this repetitive mess later on by making a function that takes in a function operating on info and
	// returning the relevant data and returns a handler
	mux.HandleFunc("/api/getnumoftxs", func(w http.ResponseWriter, r *http.Request) {
		info, err := c.GetInfo()
		if err != nil {
			return
		}
		_, err = w.Write([]byte(toJSON(info.Number)))
		if err != nil { // write to the client failed
			fmt.Println("Got error while sending API data", err)
		}
	})
	mux.HandleFunc("/api/amount", func(w http.ResponseWriter, r *http.Request) {
		info, err := c.GetInfo()
		if err != nil {
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
			return
		}
		_, err = w.Write([]byte(toJSON(info.Recent)))
		if err != nil { // write to the client failed
			fmt.Println("Got error while sending API data", err)
		}
	})
	go func() {
		err := http.ListenAndServe("0.0.0.0:"+os.Getenv("PORT"), mux)
		if err != nil {
			fmt.Println("Serve error", err)
			os.Exit(127)
		}
	}()
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
	txChan, err := f.GetTXChan("") // TODO: replace this with last TX ID in db
	if err != nil {
		return nil, err
	}
	go func() {
		for tx := range txChan {
			result.lock.Lock()
			// TODO: use tx to keep adding stuff to the db
			result.lock.Unlock()
		}
	}()
	return result, nil
}

func (s *SQLiteStore) GetTotalAmount() (float64, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	// TODO: do a query of the db and get the total amount (maybe cache it for next time?)
}

func (s *SQLiteStore) GetNumOfTXs() (int64, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	// TODO: do a query of the db and get the num of TXs
}

func (s *SQLiteStore) GetLargestDonation() (TX, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	// TODO: do a query of the db and get the largest tx
}

func (s *SQLiteStore) GetRecentDonation() (TX, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	// TODO: do a query of the db and get the largest tx
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
