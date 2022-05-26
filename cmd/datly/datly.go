package main

import (
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/viant/afs/embed"
	_ "github.com/viant/afsc/aws"
	_ "github.com/viant/afsc/gcp"
	_ "github.com/viant/afsc/gs"
	_ "github.com/viant/afsc/s3"
	_ "github.com/viant/bigquery"
	"github.com/viant/datly/cmd"
	_ "github.com/viant/scy/kms/blowfish"
	_ "github.com/viant/sqlx/metadata/product/bigquery"
	_ "github.com/viant/sqlx/metadata/product/mysql"
	_ "github.com/viant/sqlx/metadata/product/pg"
	"strings"
)

func main() {
	cmd.Run(strings.Split("-D=mysql  -N=adorder -T=CI_AD_ORDER -R=flight:CI_AD_ORDER_FLIGHT -R=audience:CI_AUDIENCE -C=ci_ads", " "))
	//cmd.Run(os.Args[1:])
}
