terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    restapi = {
      source  = "Mastercard/restapi"
      version = "3.0.0"
    }
    restate = {
      source = "restatedev/restate"
    }
  }
}
