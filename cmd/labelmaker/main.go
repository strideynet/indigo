package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/bluesky-social/indigo/carstore"
	"github.com/bluesky-social/indigo/labeler"
	"github.com/bluesky-social/indigo/util/cliutil"
	"github.com/bluesky-social/indigo/util/version"
	"github.com/urfave/cli/v2"

	_ "github.com/joho/godotenv/autoload"
	_ "go.uber.org/automaxprocs"

	logging "github.com/ipfs/go-log"
	"github.com/whyrusleeping/go-did"
	"gorm.io/plugin/opentelemetry/tracing"
)

var log = logging.Logger("labelmaker")

func main() {
	if err := run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {

	app := cli.App{
		Name:    "labelmaker",
		Usage:   "atproto content labeling daemon",
		Version: version.Version,
	}

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "db-url",
			Usage:   "database connection string for labelmaker database",
			Value:   "sqlite://./data/labelmaker/labelmaker.sqlite",
			EnvVars: []string{"DATABASE_URL"},
		},
		&cli.StringFlag{
			Name:    "carstore-db-url",
			Usage:   "database connection string for carstore database",
			Value:   "sqlite://./data/labelmaker/carstore.sqlite",
			EnvVars: []string{"CARSTORE_DATABASE_URL"},
		},
		&cli.BoolFlag{
			Name: "db-tracing",
		},
		&cli.StringFlag{
			Name:    "data-dir",
			Usage:   "path of directory for CAR files and other data",
			Value:   "data/labelmaker",
			EnvVars: []string{"DATA_DIR"},
		},
		&cli.StringFlag{
			Name:    "bgs-host",
			Usage:   "hostname and port of BGS to subscribe to",
			Value:   "localhost:2470",
			EnvVars: []string{"ATP_BGS_HOST"},
		},
		&cli.StringFlag{
			Name:    "plc-host",
			Usage:   "method, hostname, and port of PLC registry",
			Value:   "https://plc.directory",
			EnvVars: []string{"ATP_PLC_HOST"},
		},
		// TODO(bnewbold): this is a temporary hack to fetch our own blobs
		&cli.StringFlag{
			Name:    "pds-host",
			Usage:   "method, hostname, and port of PDS instance",
			Value:   "http://localhost:4849",
			EnvVars: []string{"ATP_PDS_HOST"},
		},
		&cli.BoolFlag{
			Name:  "subscribe-insecure-ws",
			Usage: "when connecting to BGS instance, use ws:// instead of wss://",
		},
		&cli.StringFlag{
			Name:    "repo-did",
			Usage:   "DID for labelmaker repo",
			Value:   "did:plc:FAKE",
			EnvVars: []string{"LABELMAKER_REPO_DID"},
		},
		&cli.StringFlag{
			Name:    "repo-handle",
			Usage:   "handle for labelmaker repo",
			Value:   "labelmaker.test",
			EnvVars: []string{"LABELMAKER_REPO_HANDLE"},
		},
		&cli.StringFlag{
			Name:    "repo-password",
			Usage:   "labelmaker repo password, used as admin password",
			Value:   "admin",
			EnvVars: []string{"LABELMAKER_REPO_PASSWORD"},
		},
		&cli.StringFlag{
			Name:    "signing-secret-key-jwk",
			Usage:   "signing key for labelmaker repo, in JWK serialization",
			EnvVars: []string{"LABELMAKER_SIGNING_SECRET_KEY_JWK"},
		},
		&cli.StringFlag{
			Name:    "bind",
			Usage:   "IP or address, and port, to listen on for HTTP and WebSocket APIs",
			Value:   ":2210",
			EnvVars: []string{"LABELMAKER_BIND"},
		},
		&cli.StringFlag{
			Name:    "xrpc-proxy-url",
			Usage:   "backend URL to proxy (some) XRPC requests to",
			Value:   "http://localhost:2583",
			EnvVars: []string{"ATP_XRPC_PROXY_URL"},
		},
		&cli.StringFlag{
			Name:    "xrpc-proxy-admin-password",
			Usage:   "admin auth password for XRPC proxy requests",
			Value:   "admin",
			EnvVars: []string{"ATP_XRPC_PROXY_ADMIN_PASSWORD"},
		},
		&cli.StringFlag{
			Name:    "keyword-file",
			Usage:   "keyword filter config, as JSON file",
			EnvVars: []string{"LABELMAKER_KEYWORD_FILE"},
		},
		&cli.StringFlag{
			Name:    "micro-nsfw-img-url",
			Usage:   "'micro-nsfw-img' classifier endpoint (full URL)",
			EnvVars: []string{"LABELMAKER_MICRO_NSFW_IMG_URL"},
		},
		&cli.StringFlag{
			Name:    "hiveai-api-token",
			Usage:   "thehive.ai API token",
			EnvVars: []string{"LABELMAKER_HIVEAI_API_TOKEN"},
		},
		&cli.StringFlag{
			Name:    "sqrl-url",
			Usage:   "SQRL API endpoint (full URL)",
			EnvVars: []string{"LABELMAKER_SQRL_URL"},
		},
		&cli.IntFlag{
			Name:    "max-carstore-connections",
			EnvVars: []string{"MAX_CARSTORE_CONNECTIONS"},
			Value:   40,
		},
		&cli.IntFlag{
			Name:    "max-metadb-connections",
			EnvVars: []string{"MAX_METADB_CONNECTIONS"},
			Value:   40,
		},
	}

	app.Action = func(cctx *cli.Context) error {

		// ensure data directory exists; won't error if it does
		datadir := cctx.String("data-dir")
		csdir := filepath.Join(datadir, "carstore")
		os.MkdirAll(datadir, os.ModePerm)
		repoKeyPath := filepath.Join(datadir, "labelmaker.key")

		dburl := cctx.String("db-url")
		db, err := cliutil.SetupDatabase(dburl, cctx.Int("max-metadb-connections"))
		if err != nil {
			return err
		}

		csdburl := cctx.String("carstore-db-url")
		csdb, err := cliutil.SetupDatabase(csdburl, cctx.Int("max-carstore-connections"))
		if err != nil {
			return err
		}

		if cctx.Bool("db-tracing") {
			if err := db.Use(tracing.NewPlugin()); err != nil {
				return err
			}
			if err := csdb.Use(tracing.NewPlugin()); err != nil {
				return err
			}
		}

		os.MkdirAll(filepath.Dir(csdir), os.ModePerm)
		cstore, err := carstore.NewCarStore(csdb, csdir)
		if err != nil {
			return err
		}

		kwlFile := cctx.String("keyword-file")
		var kwl []labeler.KeywordLabeler
		if kwlFile != "" {
			kwl, err = labeler.LoadKeywordFile(kwlFile)
			if err != nil {
				return err
			}
		} else {
			// trivial examples
			kwl = append(kwl, labeler.KeywordLabeler{Value: "meta", Keywords: []string{"bluesky", "atproto"}})
			kwl = append(kwl, labeler.KeywordLabeler{Value: "wordle", Keywords: []string{"wordle"}})
			kwl = append(kwl, labeler.KeywordLabeler{Value: "definite-article", Keywords: []string{"the"}})
		}

		bgsURL := cctx.String("bgs-host")
		plcURL := cctx.String("plc-host")
		blobPdsURL := cctx.String("pds-host")
		useWss := !cctx.Bool("subscribe-insecure-ws")
		repoDid := cctx.String("repo-did")
		repoHandle := cctx.String("repo-handle")
		repoPassword := cctx.String("repo-password")
		signingSecretKeyJwk := cctx.String("signing-secret-key-jwk")
		bind := cctx.String("bind")
		xrpcProxyURL := cctx.String("xrpc-proxy-url")
		xrpcProxyAdminPassword := cctx.String("xrpc-proxy-admin-password")
		microNSFWImgURL := cctx.String("micro-nsfw-img-url")
		hiveAIToken := cctx.String("hiveai-api-token")
		sqrlURL := cctx.String("sqrl-url")

		if repoPassword == "admin" {
			log.Warn("using insecure default admin password (ok for dev, not for deployment)")
		}

		var serkey *did.PrivKey
		if signingSecretKeyJwk != "" {
			serkey, err = labeler.ParseSecretKey(signingSecretKeyJwk)
			if err != nil {
				return err
			}
		} else {
			serkey, err = labeler.LoadOrCreateKeyFile(repoKeyPath, "auto-labelmaker")
			if err != nil {
				return err
			}
		}

		repoUser := labeler.RepoConfig{
			Handle:     repoHandle,
			Did:        repoDid,
			Password:   repoPassword,
			SigningKey: serkey,
			UserId:     1,
		}

		srv, err := labeler.NewServer(db, cstore, repoUser, plcURL, blobPdsURL, xrpcProxyURL, xrpcProxyAdminPassword, useWss)
		if err != nil {
			return err
		}

		for _, l := range kwl {
			srv.AddKeywordLabeler(l)
		}

		if microNSFWImgURL != "" {
			srv.AddMicroNSFWImgLabeler(microNSFWImgURL)
		}

		if hiveAIToken != "" {
			srv.AddHiveAILabeler(hiveAIToken)
		}

		if sqrlURL != "" {
			srv.AddSQRLLabeler(sqrlURL)
		}

		srv.SubscribeBGS(context.TODO(), bgsURL, useWss)
		return srv.RunAPI(bind)
	}

	return app.Run(args)
}
