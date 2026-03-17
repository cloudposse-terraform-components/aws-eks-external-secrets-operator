variable "eks" {
  type = object({
    eks_cluster_id                         = optional(string, null)
    eks_cluster_arn                        = optional(string, null)
    eks_cluster_endpoint                   = optional(string, null)
    eks_cluster_certificate_authority_data = optional(string, null)
    eks_cluster_identity_oidc_issuer       = optional(string, null)
    karpenter_iam_role_arn                 = optional(string, null)
  })
  description = "EKS cluster outputs. When set, bypasses remote-state lookup of eks/cluster."
  default     = null
  nullable    = true
}

module "eks" {
  source  = "cloudposse/stack-config/yaml//modules/remote-state"
  version = "1.8.0"

  component = var.eks_component_name

  bypass = var.eks != null

  defaults = {
    eks_cluster_id                         = try(var.eks.eks_cluster_id, null)
    eks_cluster_arn                        = try(var.eks.eks_cluster_arn, null)
    eks_cluster_endpoint                   = try(var.eks.eks_cluster_endpoint, null)
    eks_cluster_certificate_authority_data = try(var.eks.eks_cluster_certificate_authority_data, null)
    eks_cluster_identity_oidc_issuer       = try(var.eks.eks_cluster_identity_oidc_issuer, null)
    karpenter_iam_role_arn                 = try(var.eks.karpenter_iam_role_arn, null)
  }

  context = module.this.context
}

variable "account_map_environment_name" {
  type        = string
  default     = "gbl"
  description = "The name of the environment where account-map is deployed. Only used when account_map_enabled is true."
}

variable "account_map_stage_name" {
  type        = string
  default     = "root"
  description = "The name of the stage where account-map is deployed. Only used when account_map_enabled is true."
}

module "account_map" {
  source  = "cloudposse/stack-config/yaml//modules/remote-state"
  version = "1.8.0"

  component   = "account-map"
  environment = var.account_map_environment_name
  stage       = var.account_map_stage_name

  bypass = !var.account_map_enabled

  defaults = {
    full_account_map = var.account_map.full_account_map
  }

  context = module.this.context
}

