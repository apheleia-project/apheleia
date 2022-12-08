package io.apheleia;

import java.io.BufferedOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.Base64;
import java.util.List;
import java.util.Optional;
import java.util.zip.GZIPInputStream;

import org.apache.commons.compress.archivers.ArchiveEntry;
import org.apache.commons.compress.archivers.tar.TarArchiveEntry;
import org.apache.commons.compress.archivers.tar.TarArchiveInputStream;
import org.eclipse.microprofile.config.Config;
import org.eclipse.microprofile.config.ConfigProvider;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.google.cloud.tools.jib.api.Credential;
import com.google.cloud.tools.jib.api.DescriptorDigest;
import com.google.cloud.tools.jib.api.RegistryException;
import com.google.cloud.tools.jib.api.RegistryUnauthorizedException;
import com.google.cloud.tools.jib.blob.Blob;
import com.google.cloud.tools.jib.event.EventHandlers;
import com.google.cloud.tools.jib.http.FailoverHttpClient;
import com.google.cloud.tools.jib.http.ResponseException;
import com.google.cloud.tools.jib.image.json.BuildableManifestTemplate;
import com.google.cloud.tools.jib.image.json.ManifestTemplate;
import com.google.cloud.tools.jib.image.json.OciManifestTemplate;
import com.google.cloud.tools.jib.registry.ManifestAndDigest;
import com.google.cloud.tools.jib.registry.RegistryClient;

import io.quarkus.logging.Log;

public class OCIRegistryRepositoryClient {

    static final ObjectMapper MAPPER = new ObjectMapper();
    private final String registry;
    private final String owner;
    private final String repository;
    private final boolean enableHttpAndInsecureFailover;
    private final Path cacheRoot;
    private final Credential credential;

    public OCIRegistryRepositoryClient(String registry, String owner, String repository, Optional<String> authToken,
            boolean enableHttpAndInsecureFailover) {
        this.registry = registry;
        this.owner = owner;
        this.repository = repository;
        this.enableHttpAndInsecureFailover = enableHttpAndInsecureFailover;
        if (authToken.isPresent() && !authToken.get().isBlank()) {
            if (authToken.get().trim().startsWith("{")) {
                //we assume this is a .dockerconfig file
                try (var parser = MAPPER.createParser(authToken.get())) {
                    DockerConfig config = parser.readValueAs(DockerConfig.class);
                    boolean found = false;
                    String tmpUser = null;
                    String tmpPw = null;
                    String host = null;
                    for (var i : config.getAuths().entrySet()) {
                        if (registry.contains(i.getKey())) { //TODO: is contains enough?
                            found = true;
                            var decodedAuth = new String(Base64.getDecoder().decode(i.getValue().getAuth()),
                                    StandardCharsets.UTF_8);
                            int pos = decodedAuth.indexOf(":");
                            tmpUser = decodedAuth.substring(0, pos);
                            tmpPw = decodedAuth.substring(pos + 1);
                            host = i.getKey();
                            break;
                        }
                    }
                    if (!found) {
                        throw new RuntimeException("Unable to find a host matching " + registry
                                + " in provided dockerconfig, hosts provided: " + config.getAuths().keySet());
                    }
                    credential = Credential.from(tmpUser, tmpPw);
                    Log.infof("Credential provided as .dockerconfig, selected host %s for registry %s", host, registry);
                } catch (IOException e) {
                    throw new RuntimeException(e);
                }
            } else {
                var decoded = new String(Base64.getDecoder().decode(authToken.get()), StandardCharsets.UTF_8);
                int pos = decoded.indexOf(":");
                credential = Credential.from(decoded.substring(0, pos), decoded.substring(pos + 1));
                Log.infof("Credential provided as base64 encoded token");
            }
        } else {
            credential = null;
            Log.infof("No credential provided");
        }
        Config config = ConfigProvider.getConfig();
        Path cachePath = config.getValue("cache-path", Path.class);
        try {
            this.cacheRoot = Files.createDirectories(Paths.get(cachePath.toAbsolutePath().toString(), HACBS));
            Log.debugf(" Using [%s] as local cache folder", cacheRoot);
        } catch (IOException ex) {
            throw new RuntimeException("could not create cache directory", ex);
        }
    }

    public Optional<Path> extractImage(String image) {
        RegistryClient registryClient = getRegistryClient();
        registryClient.configureBasicAuth();

        try {
            ManifestAndDigest<ManifestTemplate> manifestAndDigest = registryClient.pullManifest(image, ManifestTemplate.class);

            ManifestTemplate manifest = manifestAndDigest.getManifest();
            DescriptorDigest descriptorDigest = manifestAndDigest.getDigest();

            String digestHash = descriptorDigest.getHash();
            return getLocalCachePath(registryClient, manifest, digestHash);
        } catch (RegistryUnauthorizedException ioe) {
            Log.errorf("Failed to authenticate against registry %s/%s/%s", registry, owner, repository);
            return Optional.empty();
        } catch (IOException | RegistryException ioe) {
            Throwable cause = ioe.getCause();
            while (cause != null) {
                if (cause instanceof ResponseException) {
                    ResponseException e = (ResponseException) cause;
                    if (e.getStatusCode() == 404) {
                        Log.debugf("Failed to find image %s", image);
                        return Optional.empty();
                    }
                }
                cause = cause.getCause();
            }
            throw new RuntimeException(ioe);
        }
    }

    private RegistryClient getRegistryClient() {
        RegistryClient.Factory factory = RegistryClient.factory(new EventHandlers.Builder().build(), registry,
                owner + "/" + repository,
                new FailoverHttpClient(enableHttpAndInsecureFailover, false, s -> Log.info(s.getMessage())));

        if (credential != null) {
            factory.setCredential(credential);
        }

        return factory.newRegistryClient();
    }

    private Optional<Path> getLocalCachePath(RegistryClient registryClient, ManifestTemplate manifest, String digestHash)
            throws IOException {
        Path digestHashPath = cacheRoot.resolve(digestHash);
        if (existInLocalCache(digestHashPath)) {
            return Optional.of(Paths.get(digestHashPath.toString(), ARTIFACTS));
        } else {
            return pullFromRemoteAndCache(registryClient, manifest, digestHash, digestHashPath);
        }
    }

    private Optional<Path> pullFromRemoteAndCache(RegistryClient registryClient, ManifestTemplate manifest, String digestHash,
            Path digestHashPath)
            throws IOException {
        String manifestMediaType = manifest.getManifestMediaType();

        if (OCI_MEDIA_TYPE.equalsIgnoreCase(manifestMediaType)) {
            List<BuildableManifestTemplate.ContentDescriptorTemplate> layers = ((OciManifestTemplate) manifest).getLayers();
            if (layers.size() == 3) {
                // Layer 2 is artifacts
                BuildableManifestTemplate.ContentDescriptorTemplate artifactsLayer = layers.get(2);

                Blob blob = registryClient.pullBlob(artifactsLayer.getDigest(), s -> {
                }, s -> {
                });

                Path outputPath = Files.createDirectories(digestHashPath);

                Path tarFile = Files.createFile(Paths.get(outputPath.toString(), digestHash + ".tar"));
                try (OutputStream tarOutputStream = Files.newOutputStream(tarFile)) {
                    blob.writeTo(tarOutputStream);
                }
                try (InputStream tarInput = Files.newInputStream(tarFile)) {
                    extractTarArchive(tarInput, outputPath.toString());
                    return Optional.of(Paths.get(outputPath.toString(), ARTIFACTS));
                }
            } else {
                Log.warnf("Unexpexted layer size %d. We expext 3", layers.size());
                return Optional.empty();
            }
        } else {
            // TODO: handle docker type?
            // application/vnd.docker.distribution.manifest.v2+json = V22ManifestTemplate
            throw new RuntimeException(
                    "Wrong ManifestMediaType type. We support " + OCI_MEDIA_TYPE + ", but got " + manifestMediaType);
        }
    }

    private boolean existInLocalCache(Path digestHashPath) {
        return Files.exists(digestHashPath) && Files.isDirectory(digestHashPath);
    }

    private Optional<String> getSha1(Path file) throws IOException {
        Path shaFile = Paths.get(file.toString() + DOT + SHA_1);
        boolean exists = Files.exists(shaFile);
        if (exists) {
            return Optional.of(Files.readString(shaFile));
        }
        return Optional.empty();
    }

    private void extractTarArchive(InputStream tarInput, String folder) throws IOException {
        try (GZIPInputStream inputStream = new GZIPInputStream(tarInput);
                TarArchiveInputStream tarArchiveInputStream = new TarArchiveInputStream(inputStream)) {

            for (TarArchiveEntry entry = tarArchiveInputStream.getNextTarEntry(); entry != null; entry = tarArchiveInputStream
                    .getNextTarEntry()) {
                extractEntry(entry, tarArchiveInputStream, folder);
            }
        }
    }

    private void extractEntry(ArchiveEntry entry, InputStream tar, String folder) throws IOException {
        final int bufferSize = 4096;
        final String path = folder + File.separator + entry.getName();
        if (entry.isDirectory()) {
            new File(path).mkdirs();
        } else {
            int count;
            byte[] data = new byte[bufferSize];
            try (FileOutputStream os = new FileOutputStream(path);
                    BufferedOutputStream dest = new BufferedOutputStream(os, bufferSize)) {
                while ((count = tar.read(data, 0, bufferSize)) != -1) {
                    dest.write(data, 0, count);
                }
            }
        }
    }

    private static final String UNDERSCORE = "_";
    private static final String HACBS = "hacbs";
    private static final String ARTIFACTS = "artifacts";
    private static final String DOT = ".";
    private static final String SHA_1 = "sha1";
    private static final String OCI_MEDIA_TYPE = "application/vnd.oci.image.manifest.v1+json";
}
