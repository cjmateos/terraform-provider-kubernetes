package kubernetes

import (
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
	"k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pkgApi "k8s.io/apimachinery/pkg/types"
	api "k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/apis/extensions/v1beta1"
	kubernetes "k8s.io/kubernetes/pkg/client/clientset_generated/clientset"
)

func resourceKubernetesIngress() *schema.Resource {
	return &schema.Resource{
		Create: resourceKubernetesIngressCreate,
		Read:   resourceKubernetesIngressRead,
		Exists: resourceKubernetesIngressExists,
		Update: resourceKubernetesIngressUpdate,
		Delete: resourceKubernetesIngressDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"metadata": namespacedMetadataSchema("ingress", true),
			"spec": {
				Type:        schema.TypeList,
				Description: "Spec defines the behavior of an ingress. https://github.com/kubernetes/community/blob/master/contributors/devel/api-conventions.md#spec-and-status",
				Required:    true,
				MaxItems:    1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"backend": {
							Type:        schema.TypeList,
							Description: "A default backend capable of servicing requests that don't match any rule. At least one of 'backend' or 'rules' must be specified. This field is optional to allow the loadbalancer controller or defaulting logic to specify a global default.",
							MaxItems:    1,
							Optional:    true,
							Elem:        backendSpecFields(),
						},
						"rule": {
							Type:        schema.TypeList,
							Description: "A default backend capable of servicing requests that don't match any rule. At least one of 'backend' or 'rules' must be specified. This field is optional to allow the loadbalancer controller or defaulting logic to specify a global default.",
							Optional:    true,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"host": {
										Type:        schema.TypeString,
										Description: "Host is the fully qualified domain name of a network host, as defined by RFC 3986. Note the following deviations from the \"host\" part of the URI as defined in the RFC: 1. IPs are not allowed. Currently an IngressRuleValue can only apply to the IP in the Spec of the parent Ingress. 2. The : delimiter is not respected because ports are not allowed. Currently the port of an Ingress is implicitly :80 for http and :443 for https. Both these may change in the future. Incoming requests are matched against the host before the IngressRuleValue. If the host is unspecified, the Ingress routes all traffic based on the specified IngressRuleValue.",
										Optional:    true,
									},
									"http": {
										Type:        schema.TypeList,
										MaxItems:    1,
										Description: "http is a list of http selectors pointing to backends. In the example: http:///? -> backend where where parts of the url correspond to RFC 3986, this resource will be used to match against everything after the last '/' and before the first '?' or '#'.",
										Elem: &schema.Resource{
											Schema: map[string]*schema.Schema{
												"path": {
													Type:        schema.TypeList,
													Description: "Path array of path regex associated with a backend. Incoming urls matching the path are forwarded to the backend.",
													Elem: &schema.Resource{
														Schema: map[string]*schema.Schema{
															"path": {
																Type:        schema.TypeString,
																Description: "Path is an extended POSIX regex as defined by IEEE Std 1003.1, (i.e this follows the egrep/unix syntax, not the perl syntax) matched against the path of an incoming request. Currently it can contain characters disallowed from the conventional \"path\" part of a URL as defined by RFC 3986. Paths must begin with a '/'. If unspecified, the path defaults to a catch all sending traffic to the backend.",
																Required:    true,
															},
															"backend": {
																Type:        schema.TypeList,
																Description: "Backend defines the referenced service endpoint to which the traffic will be forwarded to.",
																MaxItems:    1,
																Optional:    true,
																Elem:        backendSpecFields(),
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
						"tls": {
							Type:        schema.TypeList,
							Description: "TLS configuration. Currently the Ingress only supports a single TLS port, 443. If multiple members of this list specify different hosts, they will be multiplexed on the same port according to the hostname specified through the SNI TLS extension, if the ingress controller fulfilling the ingress supports SNI.",
							Optional:    true,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"host": {
										Type:        schema.TypeList,
										Description: "Hosts are a list of hosts included in the TLS certificate. The values in this list must match the name/s used in the tlsSecret. Defaults to the wildcard host setting for the loadbalancer controller fulfilling this Ingress, if left unspecified.",
										Optional:    true,
										Elem:        schema.TypeString,
									},
									"secret_name": {
										Type:        schema.TypeString,
										Description: "SecretName is the name of the secret used to terminate SSL traffic on 443. Field is left optional to allow SSL routing based on SNI hostname alone. If the SNI host in a listener conflicts with the \"Host\" header field used by an IngressRule, the SNI host is used for termination and value of the Host header is used for routing.",
										Required:    true,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func resourceKubernetesIngressCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetes.Clientset)

	metadata := expandMetadata(d.Get("metadata").([]interface{}))
	ing := v1beta1.Ingress{
		Spec: expandIngressSpec(d.Get("spec").([]interface{})),
	}
	ing.ObjectMeta = metadata
	log.Printf("[INFO] Creating new ingress: %#v", ing)
	out, err := conn.ExtensionsV1beta1().Ingresses(namespace).Create(ing)
	// out, err := conn.CoreV1().Services(metadata.Namespace).Create(&ing)
	if err != nil {
		return err
	}
	log.Printf("[INFO] Submitted new service: %#v", out)
	d.SetId(buildId(out.ObjectMeta))

	if out.Spec.Type == api.ServiceTypeLoadBalancer {
		log.Printf("[DEBUG] Waiting for load balancer to assign IP/hostname")

		err = resource.Retry(10*time.Minute, func() *resource.RetryError {
			ing, err := conn.CoreV1().Services(out.Namespace).Get(out.Name, meta_v1.GetOptions{})
			if err != nil {
				log.Printf("[DEBUG] Received error: %#v", err)
				return resource.NonRetryableError(err)
			}

			lbIngress := ing.Status.LoadBalancer.Ingress

			log.Printf("[INFO] Received service status: %#v", ing.Status)
			if len(lbIngress) > 0 {
				return nil
			}

			return resource.RetryableError(fmt.Errorf(
				"Waiting for service %q to assign IP/hostname for a load balancer", d.Id()))
		})
		if err != nil {
			lastWarnings, wErr := getLastWarningsForObject(conn, out.ObjectMeta, "Service", 3)
			if wErr != nil {
				return wErr
			}
			return fmt.Errorf("%s%s", err, stringifyEvents(lastWarnings))
		}
	}

	return resourceKubernetesIngressRead(d, meta)
}

func resourceKubernetesIngressRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetes.Clientset)

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return err
	}

	log.Printf("[INFO] Reading ingress %s", name)
	ing, err := conn.CoreV1().Ingresses(namespace).Get(name, meta_v1.GetOptions{})
	if err != nil {
		log.Printf("[DEBUG] Received error: %#v", err)
		return err
	}
	log.Printf("[INFO] Received ingress: %#v", ing)
	err = d.Set("metadata", flattenMetadata(ing.ObjectMeta))
	if err != nil {
		return err
	}

	flattened := flattenIngressSpec(ing.Spec)
	log.Printf("[DEBUG] Flattened ingress spec: %#v", flattened)
	err = d.Set("spec", flattened)
	if err != nil {
		return err
	}

	return nil
}

func resourceKubernetesIngressUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetes.Clientset)

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return err
	}

	ops := patchMetadata("metadata.0.", "/metadata/", d)
	if d.HasChange("spec") {
		diffOps := patchIngressSpec("spec.0.", "/spec/", d)
		ops = append(ops, diffOps...)
	}
	data, err := ops.MarshalJSON()
	if err != nil {
		return fmt.Errorf("Failed to marshal update operations: %s", err)
	}
	log.Printf("[INFO] Updating ingress %q: %v", name, string(data))
	out, err := conn.CoreV1().Ingresses(namespace).Patch(name, pkgApi.JSONPatchType, data)
	if err != nil {
		return fmt.Errorf("Failed to update ingress: %s", err)
	}
	log.Printf("[INFO] Submitted updated ingress: %#v", out)
	d.SetId(buildId(out.ObjectMeta))

	return resourceKubernetesIngressRead(d, meta)
}

func resourceKubernetesIngressDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetes.Clientset)

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return err
	}

	log.Printf("[INFO] Deleting ingress: %#v", name)
	err = conn.CoreV1().Ingresses(namespace).Delete(name, &meta_v1.DeleteOptions{})
	if err != nil {
		return err
	}

	log.Printf("[INFO] Ingress %s deleted", name)

	d.SetId("")
	return nil
}

func resourceKubernetesIngressExists(d *schema.ResourceData, meta interface{}) (bool, error) {
	conn := meta.(*kubernetes.Clientset)

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return false, err
	}

	log.Printf("[INFO] Checking ingress %s", name)
	_, err = conn.CoreV1().Ingresses(namespace).Get(name, meta_v1.GetOptions{})
	if err != nil {
		if statusErr, ok := err.(*errors.StatusError); ok && statusErr.ErrStatus.Code == 404 {
			return false, nil
		}
		log.Printf("[DEBUG] Received error: %#v", err)
	}
	return true, err
}
