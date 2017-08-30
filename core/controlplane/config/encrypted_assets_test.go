package config

import (
	"testing"

	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"github.com/kubernetes-incubator/kube-aws/model"
	"github.com/kubernetes-incubator/kube-aws/test/helper"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func genAssets(t *testing.T) *RawAssetsOnMemory {
	cluster, err := ClusterFromBytes([]byte(singleAzConfigYaml))
	if err != nil {
		t.Fatalf("failed generating config: %v", err)
	}

	caKey, caCert, err := cluster.NewTLSCA()
	if err != nil {
		t.Fatalf("failed generating tls ca: %v", err)
	}
	assets, err := cluster.NewAssetsOnMemory(caKey, caCert)
	if err != nil {
		t.Fatalf("failed generating assets: %v", err)
	}

	return assets
}

func TestTLSGeneration(t *testing.T) {
	assets := genAssets(t)

	pairs := []*struct {
		Name      string
		KeyBytes  []byte
		CertBytes []byte
		Key       *rsa.PrivateKey
		Cert      *x509.Certificate
	}{
		//CA MUST come first
		{
			Name:      "ca",
			KeyBytes:  assets.CAKey,
			CertBytes: assets.CACert,
		},
		{
			Name:      "apiserver",
			KeyBytes:  assets.APIServerKey,
			CertBytes: assets.APIServerCert,
		},
		{
			Name:      "admin",
			KeyBytes:  assets.AdminKey,
			CertBytes: assets.AdminCert,
		},
		{
			Name:      "worker",
			KeyBytes:  assets.WorkerKey,
			CertBytes: assets.WorkerCert,
		},
		{
			Name:      "etcd",
			KeyBytes:  assets.EtcdKey,
			CertBytes: assets.EtcdCert,
		},
	}

	var err error
	for _, pair := range pairs {

		if keyBlock, _ := pem.Decode(pair.KeyBytes); keyBlock == nil {
			t.Errorf("Failed decoding pem block from %s", pair.Name)
		} else {
			pair.Key, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
			if err != nil {
				t.Errorf("Failed to parse key %s : %v", pair.Name, err)
			}
		}

		if certBlock, _ := pem.Decode(pair.CertBytes); certBlock == nil {
			t.Errorf("Failed decoding pem block from %s", pair.Name)
		} else {
			pair.Cert, err = x509.ParseCertificate(certBlock.Bytes)
			if err != nil {
				t.Errorf("Failed to parse cert %s: %v", pair.Name, err)
			}
		}
	}

	t.Log("Assets assets parsed successfully")

	if t.Failed() {
		t.Fatalf("Assets key pairs not parsed, cannot verify signatures")
	}

	caCert := pairs[0].Cert
	for _, pair := range pairs[1:] {
		if err := pair.Cert.CheckSignatureFrom(caCert); err != nil {
			t.Errorf(
				"Could not verify ca certificate signature %s : %v",
				pair.Name,
				err)
		}
	}
}

func TestReadOrCreateCompactAssets(t *testing.T) {
	helper.WithDummyCredentials(func(dir string) {
		kmsConfig := KMSConfig{
			KMSKeyARN:      "keyarn",
			Region:         model.RegionForName("us-west-1"),
			EncryptService: &dummyEncryptService{},
		}

		// See https://github.com/kubernetes-incubator/kube-aws/issues/107
		t.Run("CachedToPreventUnnecessaryNodeReplacement", func(t *testing.T) {
			created, err := ReadOrCreateCompactAssets(dir, true, kmsConfig)

			if err != nil {
				t.Errorf("failed to read or update compact assets in %s : %v", dir, err)
				t.FailNow()
			}

			// This depends on TestDummyEncryptService which ensures dummy encrypt service to produce different ciphertext for each encryption
			// created == read means that encrypted assets were loaded from cached files named *.pem.enc, instead of re-encrypting raw assets named *.pem files
			// TODO Use some kind of mocking framework for tests like this
			read, err := ReadOrCreateCompactAssets(dir, true, kmsConfig)

			if err != nil {
				t.Errorf("failed to read or update compact assets in %s : %v", dir, err)
				t.FailNow()
			}

			if !reflect.DeepEqual(created, read) {
				t.Errorf(`failed to content encrypted assets.
	encrypted assets must not change after their first creation but they did change:
	created = %v
	read = %v`, created, read)
			}
		})

		t.Run("RemoveFilesToRegenerate", func(t *testing.T) {
			original, err := ReadOrCreateCompactAssets(dir, true, kmsConfig)

			if err != nil {
				t.Errorf("failed to read the original encrypted assets : %v", err)
				t.FailNow()
			}

			files := []string{
				"ca-key.pem.enc", "admin-key.pem.enc", "worker-key.pem.enc", "apiserver-key.pem.enc",
				"etcd-key.pem.enc", "etcd-client-key.pem.enc",
			}

			for _, filename := range files {
				if err := os.Remove(filepath.Join(dir, filename)); err != nil {
					t.Errorf("failed to remove %s for test setup : %v", filename, err)
					t.FailNow()
				}
			}

			regenerated, err := ReadOrCreateCompactAssets(dir, true, kmsConfig)

			if err != nil {
				t.Errorf("failed to read the regenerated encrypted assets : %v", err)
				t.FailNow()
			}

			for _, v := range [][]string{
				{"AdminCert", original.AdminCert, regenerated.AdminCert},
				{"CACert", original.CACert, regenerated.CACert},
				{"WorkerCert", original.WorkerCert, regenerated.WorkerCert},
				{"APIServerCert", original.APIServerCert, regenerated.APIServerCert},
				{"EtcdClientCert", original.EtcdClientCert, regenerated.EtcdClientCert},
				{"EtcdCert", original.EtcdCert, regenerated.EtcdCert},
			} {
				if v[1] != v[2] {
					t.Errorf("%s must NOT change but it did : original = %v, regenrated = %v ", v[0], v[1], v[2])
				}
			}

			for _, v := range [][]string{
				{"AdminKey", original.AdminKey, regenerated.AdminKey},
				{"CAKey", original.CAKey, regenerated.CAKey},
				{"WorkerKey", original.WorkerKey, regenerated.WorkerKey},
				{"APIServerKey", original.APIServerKey, regenerated.APIServerKey},
				{"EtcdClientKey", original.EtcdClientKey, regenerated.EtcdClientKey},
				{"EtcdKey", original.EtcdKey, regenerated.EtcdKey},
			} {
				if v[1] == v[2] {
					t.Errorf("%s must change but it didn't : original = %v, regenrated = %v ", v[0], v[1], v[2])
				}
			}
			if reflect.DeepEqual(original, regenerated) {
				t.Errorf(`unexpecteed data contained in (possibly) regenerated encrypted assets.
	encrypted assets must change after regeneration but they didn't:
	original = %v
	regenerated = %v`, original, regenerated)
			}
		})
	})
}

func TestReadOrCreateUnEncryptedCompactAssets(t *testing.T) {
	helper.WithDummyCredentials(func(dir string) {
		t.Run("CachedToPreventUnnecessaryNodeReplacementOnUnencrypted", func(t *testing.T) {
			created, err := ReadOrCreateUnencryptedCompactAssets(dir, true)

			if err != nil {
				t.Errorf("failed to read or update compact assets in %s : %v", dir, err)
			}

			read, err := ReadOrCreateUnencryptedCompactAssets(dir, true)

			if err != nil {
				t.Errorf("failed to read or update compact assets in %s : %v", dir, err)
			}

			if !reflect.DeepEqual(created, read) {
				t.Errorf(`failed to content unencrypted assets.
 	unencrypted assets must not change after their first creation but they did change:
 	created = %v
 	read = %v`, created, read)
			}
		})
	})
}

func TestRandomTLSBootstrapTokenString(t *testing.T) {
	randomToken, err := RandomTLSBootstrapTokenString()
	if err != nil {
		t.Errorf("failed to generate a Kubelet bootstrap token: %v", err)
	}
	if strings.Index(randomToken, ",") >= 0 {
		t.Errorf("random token not expect to contain a comma: %v", randomToken)
	}

	b, err := base64.URLEncoding.DecodeString(randomToken)
	if err != nil {
		t.Errorf("failed to decode base64 token string: %v", err)
	}
	if len(b) != 256 {
		t.Errorf("expected token to be 256 bits long, but was %d", len(b))
	}
}

func TestHasAuthTokens(t *testing.T) {
	testCases := []struct {
		authTokens string
		expected   bool
	}{
		// Without auth tokens
		{
			authTokens: "",
			expected:   false,
		},

		// With auth tokens
		{
			authTokens: "contents",
			expected:   true,
		},
	}

	for _, testCase := range testCases {
		asset := &CompactAssets{
			AuthTokens: testCase.authTokens,
		}

		actual := asset.HasAuthTokens()
		if actual != testCase.expected {
			t.Errorf("Expected HasAuthTokens to be %v, but was %v", testCase.expected, actual)
		}
	}
}

func TestHasTLSBootstrapToken(t *testing.T) {
	testCases := []struct {
		tlsBootstrapToken string
		expected          bool
	}{
		// Without TLS bootstrap token
		{
			tlsBootstrapToken: "",
			expected:          false,
		},

		// With TLS bootstrap token
		{
			tlsBootstrapToken: "contents",
			expected:          true,
		},
	}

	for _, testCase := range testCases {
		asset := &CompactAssets{
			TLSBootstrapToken: testCase.tlsBootstrapToken,
		}

		actual := asset.HasTLSBootstrapToken()
		if actual != testCase.expected {
			t.Errorf("Expected HasTLSBootstrapToken to be %v, but was %v", testCase.expected, actual)
		}
	}
}