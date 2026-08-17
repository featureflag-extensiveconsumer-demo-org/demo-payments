package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ld "github.com/launchdarkly/go-server-sdk/v7"
)

const repository = "demo-payments"
const release = "v001"

var flags = []string{"demo-checkout-rollout", "demo-fraud-screening", "demo-payment-retry"}

const topologyJSON = `{"production":[{"key":"prod-eu-west-02","name":"Production EU West 02","environment":"production","region":"eu-west","ordinal":2,"releaseRing":"canary","weight":5},{"key":"prod-sa-east-02","name":"Production South America East 02","environment":"production","region":"sa-east","ordinal":2,"releaseRing":"stable","weight":10},{"key":"prod-us-east-02","name":"Production US East 02","environment":"production","region":"us-east","ordinal":2,"releaseRing":"stable","weight":15},{"key":"prod-emea-central-04","name":"Production EMEA Central 04","environment":"production","region":"emea-central","ordinal":4,"releaseRing":"stable","weight":30},{"key":"prod-eu-west-01","name":"Production EU West 01","environment":"production","region":"eu-west","ordinal":1,"releaseRing":"stable","weight":40}],"staging":[{"key":"stg-eu-central-02","name":"Staging EU Central 02","environment":"staging","region":"eu-central","ordinal":2,"releaseRing":"canary","weight":40},{"key":"stg-eu-central-01","name":"Staging EU Central 01","environment":"staging","region":"eu-central","ordinal":1,"releaseRing":"stable","weight":60}],"test":[{"key":"test-eu-central-02","name":"Test EU Central 02","environment":"test","region":"eu-central","ordinal":2,"releaseRing":"canary","weight":25},{"key":"test-eu-central-01","name":"Test EU Central 01","environment":"test","region":"eu-central","ordinal":1,"releaseRing":"stable","weight":75}],"dev":[{"key":"dev-local-01","name":"Development Local 01","environment":"dev","region":"local","ordinal":1,"releaseRing":"stable","weight":100}]}`
const profilesJSON = `{"production":{"enterprise":10,"beta":15,"legacy":8,"busy":100,"quiet":40},"staging":{"enterprise":20,"beta":30,"legacy":20,"busy":30,"quiet":12},"test":{"enterprise":30,"beta":35,"legacy":30,"busy":10,"quiet":4},"dev":{"enterprise":15,"beta":25,"legacy":12,"busy":2,"quiet":1}}`
const offsetsJSON = `{"demo-orders":11,"demo-storefront":43,"demo-profile":71}`

type cluster struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Region      string `json:"region"`
	Ordinal     int    `json:"ordinal"`
	ReleaseRing string `json:"releaseRing"`
	Weight      int    `json:"weight"`
}

type profile struct {
	Enterprise int `json:"enterprise"`
	Beta       int `json:"beta"`
	Legacy     int `json:"legacy"`
	Busy       int `json:"busy"`
	Quiet      int `json:"quiet"`
}

var clusters map[string][]cluster
var profiles map[string]profile
var offsets map[string]int

func offsetFor(name string) int {
	if value, ok := offsets[name]; ok {
		return value
	}
	hash := 7
	for _, char := range name {
		hash = (hash*31 + int(char)) % 100
	}
	return hash
}

func batchSize(name string, at time.Time) int {
	settings := profiles[name]
	day := int(at.UTC().Weekday())
	hour := at.UTC().Hour()
	if day >= 1 && day <= 5 && hour >= 7 && hour < 19 {
		return settings.Busy
	}
	return settings.Quiet
}

func clusterFor(name string, env string, index int) cluster {
	choices := clusters[env]
	bucket := (index*17 + offsetFor(name)) % 100
	boundary := 0
	for _, item := range choices {
		boundary += item.Weight
		if bucket < boundary {
			return item
		}
	}
	return choices[len(choices)-1]
}

func contextForTraffic(name string, env string, index int, generation string) ldcontext.Context {
	settings := profiles[env]
	bucket := (index*37 + offsetFor(name)) % 100
	plan, region, cohort := "free", "eu", "control"
	if bucket < settings.Enterprise {
		plan = "enterprise"
	} else if bucket < settings.Enterprise+settings.Beta {
		cohort = "checkout-beta"
	}
	user := ldcontext.NewBuilder(fmt.Sprintf("%s-%s-%d", name, env, index%1000)).Kind("user").
		SetString("plan", plan).SetString("region", region).SetString("cohort", cohort).Build()
	selected := clusterFor(name, env, index)
	service := ldcontext.NewBuilder(name).Kind("service").SetString("name", name).Build()
	clusterContext := ldcontext.NewBuilder(selected.Key).Kind("cluster").
		SetString("name", selected.Name).SetString("environment", selected.Environment).
		SetString("region", selected.Region).SetValue("ordinal", ldvalue.Int(selected.Ordinal)).
		SetString("releaseRing", selected.ReleaseRing).SetString("generation", generation).Build()
	return ldcontext.NewMulti(user, service, clusterContext)
}

func main() {
	if err := json.Unmarshal([]byte(topologyJSON), &clusters); err != nil {
		panic(err)
	}
	if err := json.Unmarshal([]byte(profilesJSON), &profiles); err != nil {
		panic(err)
	}
	if err := json.Unmarshal([]byte(offsetsJSON), &offsets); err != nil {
		panic(err)
	}
	sdkKey := os.Getenv("LD_EVALUATION_SDK_KEY")
	if sdkKey == "" {
		fmt.Fprintln(os.Stderr, "LD_EVALUATION_SDK_KEY is required.")
		os.Exit(1)
	}
	env := os.Getenv("DEMO_ENVIRONMENT")
	for position, argument := range os.Args {
		if argument == "--profile" && position+1 < len(os.Args) {
			env = os.Args[position+1]
		}
	}
	if _, ok := profiles[env]; !ok {
		fmt.Fprintln(os.Stderr, "A valid --profile is required.")
		os.Exit(1)
	}
	generation := os.Getenv("DEMO_GENERATION_ID")
	if generation == "" {
		generation = "untracked"
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	index := 0
	for {
		select {
		case <-stop:
			return
		default:
		}
		openedAt := time.Now()
		client, err := ld.MakeCustomClient(sdkKey, ld.Config{}, 10*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error: evaluator failed.")
			os.Exit(1)
		}
		count := batchSize(env, time.Now())
		perFlag := map[string]map[string]int{}
		clusterCounts := map[string]int{}
		for _, flag := range flags {
			perFlag[flag] = map[string]int{"true": 0, "false": 0}
		}
		attempted := 0
		for item := 0; item < count; item++ {
			evaluationContext := contextForTraffic(repository, env, index+item, generation)
			for _, flag := range flags {
				value, _ := client.BoolVariation(flag, evaluationContext, false)
				if value {
					perFlag[flag]["true"]++
				} else {
					perFlag[flag]["false"]++
				}
				attempted++
			}
			clusterCounts[clusterFor(repository, env, index+item).Key]++
		}
		flush := "ok"
		if !client.FlushAndWait(5 * time.Second) {
			flush = "failed"
		}
		summary := map[string]interface{}{
			"type": "traffic-batch", "repository": repository, "release": release,
			"flags": flags, "perFlag": perFlag, "profile": env, "generation": generation,
			"contexts": count, "attempted": attempted, "clusters": clusterCounts,
			"flush": flush, "connectionMs": time.Since(openedAt).Milliseconds(),
		}
		line, _ := json.Marshal(summary)
		fmt.Println(string(line))
		client.Close()
		index += count
		select {
		case <-stop:
			return
		case <-time.After(300 * time.Second):
		}
	}
}
