#!/bin/bash

# TODO: implement in Go
sqlite3 txData.db "CREATE TABLE IF NOT EXISTS TXs ( \
                    id INTEGER PRIMARY KEY AUTOINCREMENT, \
                    TXID TEXT UNIQUE NOT NULL, \
                    OutputNo INTEGER NOT NULL,\
                    ScriptAsm TEXT NOT NULL, \
                    ScriptHex TEXT NOT NULL, \
                    Value INTEGER NOT NULL, \
                    Confirmations INTEGER NOT NULL, \
                    Time INTEGER NOT NULL \
                  );"

sqlite3 txData.db "CREATE UNIQUE INDEX IF NOT EXISTS idx_txs_txid \
                    ON TXs (TXID);"