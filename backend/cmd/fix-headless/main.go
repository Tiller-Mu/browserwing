package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	bolt "go.etcd.io/bbolt"
)

func main() {
	dbPath := "data/browserwing.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	db, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("browser_instances"))
		if bucket == nil {
			return fmt.Errorf("bucket browser_instances not found")
		}

		return bucket.ForEach(func(k, v []byte) error {
			var inst map[string]interface{}
			if err := json.Unmarshal(v, &inst); err != nil {
				return err
			}

			id, _ := inst["id"].(string)
			headless, hasHeadless := inst["headless"]

			fmt.Printf("Instance: %s, headless=%v (present=%v)\n", id, headless, hasHeadless)

			if hasHeadless {
				delete(inst, "headless")
				data, err := json.Marshal(inst)
				if err != nil {
					return err
				}
				if err := bucket.Put(k, data); err != nil {
					return err
				}
				fmt.Printf("  -> Removed headless field from instance '%s'\n", id)
			}
			return nil
		})
	})

	if err != nil {
		log.Fatalf("fix failed: %v", err)
	}
	fmt.Println("Done. Headless field cleaned from all browser instances.")
}
