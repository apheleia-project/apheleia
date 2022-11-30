package io.apheleia;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.FileVisitResult;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.SimpleFileVisitor;
import java.nio.file.attribute.BasicFileAttributes;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

import javax.inject.Inject;

import org.cyclonedx.BomGeneratorFactory;
import org.cyclonedx.CycloneDxSchema;
import org.cyclonedx.generators.json.BomJsonGenerator;
import org.cyclonedx.model.Bom;
import org.cyclonedx.model.Component;
import org.cyclonedx.model.Property;

import com.redhat.hacbs.classfile.tracker.ClassFileTracker;
import com.redhat.hacbs.classfile.tracker.TrackingData;

import io.fabric8.kubernetes.client.KubernetesClient;
import io.quarkus.logging.Log;
import io.quarkus.picocli.runtime.annotations.TopCommand;
import picocli.CommandLine;

@TopCommand
@CommandLine.Command
public class AnalyserCommand implements Runnable {

    @Inject
    KubernetesClient client;

    @CommandLine.Option(names = "--allowed-sources", defaultValue = "redhat,rebuilt", split = ",")
    Set<String> allowedSources;

    @CommandLine.Option(names = "-s")
    Path sbom;

    @CommandLine.Parameters
    List<Path> paths;

    @Override
    public void run() {
        try {
            Set<String> gavs = new HashSet<>();
            Set<TrackingData> trackingData = new HashSet<>();
            doAnalysis(gavs, trackingData);
            System.out.println(trackingData);
            writeSbom(trackingData);
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    void doAnalysis(Set<String> gavs, Set<TrackingData> trackingData) throws IOException {
        Log.infof("Root paths %s", paths);

        //look for classes produced by the build
        Set<String> plainClasses = new HashSet<>();
        for (var path : paths) {
            Files.walkFileTree(path, new SimpleFileVisitor<>() {
                @Override
                public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                    if (file.getFileName().toString().endsWith(".class")) {
                        ClassFileTracker.readTrackingInformationFromClass(Files.readAllBytes(file), plainClasses::add);
                    }
                    return FileVisitResult.CONTINUE;
                }
            });
        }
        Set<String> additional = new HashSet<>();

        for (var path : paths) {
            Files.walkFileTree(path, new SimpleFileVisitor<>() {
                @Override
                public FileVisitResult visitFile(Path file, BasicFileAttributes attrs) throws IOException {
                    var fileName = file.getFileName().toString();
                    try (var contents = Files.newInputStream(file)) {
                        Log.debugf("Processing %s", fileName);
                        var jarData = ClassFileTracker.readTrackingDataFromFile(contents, fileName, (s) -> {
                            if (!plainClasses.contains(s)) {
                                System.err.println("ERROR: " + s);
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
