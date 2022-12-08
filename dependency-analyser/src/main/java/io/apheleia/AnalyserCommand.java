package io.apheleia;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.FileVisitResult;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.SimpleFileVisitor;
import java.nio.file.attribute.BasicFileAttributes;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.UnaryOperator;

import javax.enterprise.inject.Instance;
import javax.inject.Inject;

import org.cyclonedx.BomGeneratorFactory;
import org.cyclonedx.CycloneDxSchema;
import org.cyclonedx.generators.json.BomJsonGenerator;
import org.cyclonedx.model.Bom;
import org.cyclonedx.model.Component;
import org.cyclonedx.model.Property;

import com.redhat.hacbs.classfile.tracker.ClassFileTracker;
import com.redhat.hacbs.classfile.tracker.TrackingData;
import com.redhat.hacbs.resources.model.v1alpha1.ArtifactBuild;
import com.redhat.hacbs.resources.util.ResourceNameUtils;

import io.fabric8.kubernetes.client.KubernetesClient;
import io.fabric8.kubernetes.client.dsl.Resource;
import io.quarkus.logging.Log;
import io.quarkus.picocli.runtime.annotations.TopCommand;
import io.quarkus.runtime.Quarkus;
import picocli.CommandLine;

@TopCommand
@CommandLine.Command
public class AnalyserCommand implements Runnable {

    @Inject
    Instance<KubernetesClient> client;

    @CommandLine.Option(names = "--allowed-sources", defaultValue = "redhat,rebuilt", split = ",")
    Set<String> allowedSources;

    @CommandLine.Option(names = "--sbom-path")
    Path sbom;

    @CommandLine.Option(names = "--maven-repo", required = true)
    Path mavenRepo;

    @CommandLine.Option(names = "--create-artifacts")
    boolean createArtifacts;

    @CommandLine.Parameters
    List<Path> paths;

    @Override
    public void run() {
        try {
            Set<String> gavs = new HashSet<>();
            Set<TrackingData> trackingData = new HashSet<>();
            var communityDeps = doAnalysis(gavs, trackingData);
            if (createArtifacts) {
                var c = client.get();
                var abrc = c.resources(ArtifactBuild.class);
                for (var i : gavs) {
                    String name = ResourceNameUtils.nameFromGav(i);
                    Log.infof("Creating/Updating %s", name);
                    Resource<ArtifactBuild> artifactBuildResource = abrc.withName(name);
                    var abr = artifactBuildResource.get();
                    if (abr == null) {
                        abr = new ArtifactBuild();
                        abr.getMetadata().setName(name);
                        abr.getSpec().setGav(i);
                        abr.getMetadata().setAnnotations(new HashMap<>());
                        abr.getMetadata().getAnnotations().put("aphelaia.io/last-used", "" + System.currentTimeMillis());
                        c.resource(abr).createOrReplace();
                    } else {
                        artifactBuildResource.edit(new UnaryOperator<ArtifactBuild>() {
                            @Override
                            public ArtifactBuild apply(ArtifactBuild artifactBuild) {
                                if (artifactBuild.getMetadata().getAnnotations() == null) {
                                    artifactBuild.getMetadata().setAnnotations(new HashMap<>());
                                }
                                artifactBuild.getMetadata().getAnnotations().put("aphelaia.io/last-used",
                                        "" + System.currentTimeMillis());
                                return artifactBuild;
                            }
                        });
                    }
                }
            }
            if (communityDeps) {
                //exit with non-zero if there were community deps
                Quarkus.asyncExit(1);
            } else {
                //note that the SBOM is only valid when there are no community deps
                writeSbom(trackingData);
            }
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    boolean doAnalysis(Set<String> gavs, Set<TrackingData> trackingData) throws IOException {
        //scan the local maven repo first

        Map<String, List<Path>> untrackedCommunityClassesForMaven = new HashMap<>();
        Files.walkFileTree(mavenRepo, new SimpleFileVisitor<>() {
            @Override
            public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                if (file.getFileName().toString().endsWith(".jar")) {
                    ClassFileTracker.readTrackingDataFromJar(Files.readAllBytes(file), file.getFileName().toString(), (s) -> {
                        if (s.equals("module-info")) {
                            return;
                        }
                        untrackedCommunityClassesForMaven.computeIfAbsent(s, (a) -> new ArrayList<>()).add(file);
                    });
                }
                return FileVisitResult.CONTINUE;
            }
        });
        //look for classes produced by the build and remove them from the community set
        for (var path : paths) {
            Files.walkFileTree(path, new SimpleFileVisitor<>() {
                @Override
                public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                    if (file.getFileName().toString().endsWith(".class")) {
                        ClassFileTracker.readTrackingInformationFromClass(Files.readAllBytes(file),
                                untrackedCommunityClassesForMaven::remove);
                    }
                    return FileVisitResult.CONTINUE;
                }
            });
        }

        Log.infof("Root paths %s", paths);

        Set<Path> additional = new HashSet<>();

        for (var path : paths) {
            Files.walkFileTree(path, new SimpleFileVisitor<>() {
                @Override
                public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                    var fileName = file.getFileName().toString();
                    try (var contents = Files.newInputStream(file)) {
                        Log.debugf("Processing %s", fileName);
                        var jarData = ClassFileTracker.readTrackingDataFromFile(contents, fileName, (s) -> {
                            if (untrackedCommunityClassesForMaven.containsKey(s)) {
                                var jars = untrackedCommunityClassesForMaven.get(s);
                                for (var jar : jars) {
                                    if (additional.add(jar)) {
                                        Log.infof("Community jar " + jar.getFileName() + " found in " + path.relativize(file));
                                    }
                                }
                            }

                        });
                        trackingData.addAll(jarData);
                        for (var data : jarData) {
                            if (data != null) {
                                if (!allowedSources.contains(data.source)) {
                                    Log.debugf("Found GAV %s in %s", data.gav, fileName);
                                    gavs.add(data.gav);
                                }
                            }
                        }

                    }
                    return FileVisitResult.CONTINUE;

                }
            });
        }
        //now figure out the additional GAV's
        for (var i : additional) {
            boolean gradle = i.getParent().getFileName().toString().length() == 40;
            //gradle repo layout is different to maven
            //we use a different strategy to determine the GAV
            if (gradle) {
                Path version = i.getParent().getParent();
                Path artifact = version.getParent();
                Path group = artifact.getParent();
                String gav = group.getFileName() + ":" + artifact.getFileName() + ":" + version.getFileName();
                gavs.add(gav);
                trackingData.add(new TrackingData(gav, "community", Map.of()));
            } else {
                Path version = i.getParent();
                Path artifact = version.getParent();
                var group = mavenRepo.relativize(artifact.getParent()).toString().replace("/", ".");
                String gav = group + ":" + artifact.getFileName() + ":" + version.getFileName();
                gavs.add(gav);
                trackingData.add(new TrackingData(gav, "community", Map.of()));
            }
        }

        System.err.println(gavs);
        return !additional.isEmpty();
    }

    void writeSbom(Set<TrackingData> trackingData) throws IOException {
        //now build a cyclone DX bom file
        final Bom bom = new Bom();
        bom.setComponents(new ArrayList<>());
        for (var i : trackingData) {
            var split = i.gav.split(":");
            String group = split[0];
            String name = split[1];
            String version = split[2];

            Component component = new Component();
            component.setType(Component.Type.LIBRARY);
            component.setGroup(group);
            component.setName(name);
            component.setVersion(version);
            component.setPublisher(i.source);
            component.setPurl(String.format("pkg:maven/%s/%s@%s", group, name, version));

            Property packageTypeProperty = new Property();
            packageTypeProperty.setName("package:type");
            packageTypeProperty.setValue("maven");

            Property packageLanguageProperty = new Property();
            packageLanguageProperty.setName("package:language");
            packageLanguageProperty.setValue("java");

            component.setProperties(List.of(packageTypeProperty, packageLanguageProperty));

            bom.getComponents().add(component);
        }
        BomJsonGenerator generator = BomGeneratorFactory.createJson(CycloneDxSchema.Version.VERSION_14, bom);
        String sbom = generator.toJsonString();
        Log.infof("Generated SBOM:\n%s", sbom);
        if (this.sbom != null) {
            Files.writeString(this.sbom, sbom, StandardCharsets.UTF_8);
        }
    }
}
